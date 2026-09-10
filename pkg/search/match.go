package search

import (
	"context"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Engine matches queries against a corpus. Implementations must be safe for
// concurrent use.
type Engine interface {
	Find(ctx context.Context, corpus *Corpus, query string) ([]Match, error)
}

// Match is one occurrence in original resource coordinates.
type Match struct {
	ResourceIndex int
	Start         int64 // Plain byte offset of the match start.
	End           int64 // Plain byte offset just past the match end.
	Highlight     string
	Before        string
	After         string
}

// DefaultContextRunes is the minimum number of context runes captured on each
// side of a highlight.
const DefaultContextRunes = 48

// literalEngine is the default [Engine]: case-folded, NFC-normalized literal
// matching over the corpus.
type literalEngine struct{}

// NewLiteralEngine returns the default matching engine: NFC + case-folded
// literal scanning with word-boundary awareness.
func NewLiteralEngine() Engine { return literalEngine{} }

// Find returns every occurrence of query in the corpus. The query itself is
// normalized and folded the same way as the corpus, so callers may pass raw
// document text (with arbitrary whitespace) as the query. Word boundaries are
// enforced when the first/last query runes are word runes.
func (literalEngine) Find(ctx context.Context, corpus *Corpus, query string) ([]Match, error) {
	qn := foldAndNormalize(query)
	if len(qn.runes) == 0 {
		return nil, nil
	}

	var matches []Match
	for _, rt := range corpus.resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		matches = append(matches, findInResource(rt, qn)...)
	}
	return matches, nil
}

// query holds the folded+normalized form of a search query.
type query struct {
	runes []rune // folded runes
	src   []int32
}

// foldAndNormalize normalizes a raw string into folded coordinates using the
// same pipeline as resources.
func foldAndNormalize(s string) query {
	var q query
	nfc := norm.NFC.String(s)
	for bi := 0; bi < len(nfc); {
		r, size := utf8.DecodeRuneInString(nfc[bi:])
		if size == 0 {
			size = 1
		}
		if r < 0x80 && isASCIISpace(byte(r)) {
			if len(q.runes) > 0 && q.runes[len(q.runes)-1] != ' ' {
				q.runes = append(q.runes, ' ')
				q.src = append(q.src, -1)
			}
			bi += size
			continue
		}
		ni := int32(len(q.runes))
		for _, fr := range foldRune(r) {
			q.runes = append(q.runes, fr)
			q.src = append(q.src, ni)
		}
		bi += size
	}
	// Trim leading/trailing separators.
	for len(q.runes) > 0 && q.runes[0] == ' ' {
		q.runes = q.runes[1:]
		q.src = q.src[1:]
	}
	for len(q.runes) > 0 && q.runes[len(q.runes)-1] == ' ' {
		q.runes = q.runes[:len(q.runes)-1]
		q.src = q.src[:len(q.src)-1]
	}
	return q
}

// findInResource scans one resource's folded text for the query.
func findInResource(rt *resourceText, q query) []Match {
	folded := rt.foldRunes
	n := len(folded)
	m := len(q.runes)
	if m == 0 || n < m {
		return nil
	}

	firstWord := isWordRune(q.runes[0])
	lastWord := isWordRune(q.runes[m-1])

	var matches []Match
	for i := 0; i+m <= n; i++ {
		if equalFolded(folded[i:i+m], q.runes) {
			if firstWord && i > 0 && isWordRune(folded[i-1]) {
				continue
			}
			end := i + m
			if lastWord && end < n && isWordRune(folded[end]) {
				continue
			}
			match, ok := buildMatch(rt, i, end)
			if ok {
				matches = append(matches, match)
			}
			i = end - 1
		}
	}
	return matches
}

func equalFolded(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// buildMatch converts folded coordinates [i,end) to a Match with plain
// offsets and ≥DefaultContextRunes of context on each side.
func buildMatch(rt *resourceText, fi, fe int) (Match, bool) {
	si := int(rt.srcIdx[fi])
	se := int(rt.srcIdx[fe-1]) + 1

	startPlain := rt.plainOff[si]
	endPlain := plainOffsetAfter(rt, se)

	if endPlain <= startPlain {
		return Match{}, false
	}
	if startPlain < 0 || endPlain > int64(len(rt.plain)) {
		return Match{}, false
	}

	before, after := contextAround(rt, si, se, DefaultContextRunes)

	return Match{
		ResourceIndex: rt.index,
		Start:         startPlain,
		End:           endPlain,
		Highlight:     string(rt.plain[startPlain:endPlain]),
		Before:        before,
		After:         after,
	}, true
}

// plainOffsetAfter returns the plain byte offset just past norm rune index e.
func plainOffsetAfter(rt *resourceText, e int) int64 {
	if e >= len(rt.normRunes) {
		return rt.plainOff[len(rt.plainOff)-1]
	}
	return rt.plainOff[e]
}

// contextAround captures up to ctxRunes of normalized text before si and
// after se, returning it from the plain text so casing and NBSP survive.
func contextAround(rt *resourceText, si, se, ctxRunes int) (before, after string) {
	bStart := si - ctxRunes
	if bStart < 0 {
		bStart = 0
	}
	beforeFrom := rt.plainOff[bStart]
	if beforeFrom < 0 {
		beforeFrom = 0
	}
	beforeEnd := rt.plainOff[si]
	if beforeEnd > beforeFrom {
		before = trimContextEdge(string(rt.plain[beforeFrom:beforeEnd]), false)
	}

	aEndIdx := se + ctxRunes
	if aEndIdx > len(rt.normRunes) {
		aEndIdx = len(rt.normRunes)
	}
	afterTo := rt.plainOff[aEndIdx]
	if afterTo < 0 {
		afterTo = rt.plainOff[len(rt.plainOff)-1]
	}
	afterFrom := rt.plainOff[min(se, len(rt.normRunes))]
	if afterFrom < 0 {
		afterFrom = 0
	}
	if afterTo > afterFrom && afterFrom <= int64(len(rt.plain)) && afterTo <= int64(len(rt.plain)) {
		after = trimContextEdge(string(rt.plain[afterFrom:afterTo]), true)
	}
	return before, after
}

// trimContextEdge trims partial whitespace at the outer edge of a context
// snippet so snippets don't start/end mid-run.
func trimContextEdge(s string, trailing bool) string {
	const maxTrim = 16
	trimmed := s
	for range maxTrim {
		if trimmed == "" {
			break
		}
		r, size := utf8.DecodeRuneInString(trimmed)
		if !trailing {
			if isASCIISpace(byte(r)) && size == 1 {
				trimmed = trimmed[size:]
				continue
			}
		} else {
			r2, size2 := utf8.DecodeLastRuneInString(trimmed)
			if isASCIISpace(byte(r2)) && size2 == 1 {
				trimmed = trimmed[:len(trimmed)-size2]
				continue
			}
		}
		break
	}
	return strings.TrimRight(trimmed, "\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
