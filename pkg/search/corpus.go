// Package search provides a reusable, publication-agnostic search engine over
// XHTML/text resources: text extraction with position mapping, Unicode-aware
// matching, and snippet construction. It knows nothing about publications,
// services, or HTTP.
//
// Coordinates used throughout:
//
//   - plain: byte offsets into a resource's decoded text. Block-element
//     boundaries appear as '\n'; everything else is verbatim (entities are
//     decoded, markup removed).
//   - normalized ("norm"): rune indices into the NFC form of the plain text
//     with ASCII whitespace runs collapsed to single separators. Exotic
//     Unicode spaces (NBSP, en space) are content, never collapsed.
//   - folded ("fold"): rune indices into the case-folded norm text. Case
//     folding may expand one norm rune into several fold runes (e.g. ß→ss),
//     so fold positions map back through srcIdx.
//
// Matching happens in folded coordinates; results map back to plain offsets
// for snippets and locator anchoring.
package search

import (
	"bytes"
	"context"

	"golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"
	"unicode"
	"unicode/utf8"
)

// excludedElements are non-content elements whose entire subtree is dropped.
var excludedElements = map[string]bool{
	"head":     true,
	"script":   true,
	"style":    true,
	"noscript": true,
	"template": true,
}

// blockElements force a whitespace boundary in the extracted text. Inline
// elements (em, a, span, ...) do not, so phrases survive inline markup.
var blockElements = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"caption": true, "dd": true, "dl": true, "dt": true,
	"fieldset": true, "figcaption": true, "figure": true, "footer": true,
	"form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "header": true, "hr": true, "li": true,
	"main": true, "nav": true, "ol": true, "p": true, "pre": true,
	"section": true, "table": true, "td": true, "tfoot": true,
	"th": true, "thead": true, "tr": true, "ul": true,
}

func isASCIISpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}

// Resource is one searchable resource: raw markup bytes plus its reading-order
// index.
type Resource struct {
	Index int
	Href  string
	Bytes []byte
}

// DefaultMaxResourceBytes bounds extraction per resource.
const DefaultMaxResourceBytes = int64(2 << 20)

// Extractor builds searchable corpora from resources.
type Extractor struct {
	// MaxResourceBytes bounds how many raw bytes are tokenized per resource.
	// Zero selects [DefaultMaxResourceBytes].
	MaxResourceBytes int64
}

// Corpus is the searchable text of one publication.
type Corpus struct {
	resources []*resourceText
	// cumulativePlain[i] = total plain bytes of resources[0:i].
	cumulativePlain []int64
	totalPlain      int64
	// resourcePosition maps the caller-provided reading-order index to the
	// packed resource position used by resources/cumulativePlain. Resources
	// that could not be extracted are absent from the packed slices.
	resourcePosition map[int]int
}

// resourceText holds the coordinate systems of one resource.
type resourceText struct {
	index int
	href  string

	plain []byte // Decoded text; block boundaries are '\n'.

	normRunes []rune  // NFC text, ASCII whitespace collapsed.
	plainOff  []int64 // plainOff[i] = plain byte offset of normRunes[i].
	// plainOff has one extra final entry: len(plain).

	foldRunes []rune  // Case-folded normRunes.
	srcIdx    []int32 // srcIdx[j] = index into normRunes of foldRunes[j]'s source.
}

// Build extracts the corpus. A resource that fails or yields nothing
// searchable is skipped individually; cancellation aborts the whole build.
func (e Extractor) Build(ctx context.Context, resources []Resource) (*Corpus, error) {
	limit := e.MaxResourceBytes
	if limit <= 0 {
		limit = DefaultMaxResourceBytes
	}
	corpus := &Corpus{
		cumulativePlain:  []int64{0},
		resourcePosition: make(map[int]int),
	}
	for _, r := range resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw := r.Bytes
		if int64(len(raw)) > limit {
			raw = raw[:limit]
		}
		rt := extractResource(r.Index, r.Href, raw)
		if rt == nil {
			continue
		}
		corpus.resourcePosition[rt.index] = len(corpus.resources)
		corpus.resources = append(corpus.resources, rt)
		corpus.totalPlain += int64(len(rt.plain))
		corpus.cumulativePlain = append(corpus.cumulativePlain, corpus.totalPlain)
	}
	return corpus, nil
}

// Resources returns the number of extracted resources.
func (c *Corpus) Resources() int { return len(c.resources) }

// PlainLength returns the total size in bytes of all plain text.
func (c *Corpus) PlainLength() int64 { return c.totalPlain }

// ProgressionsAt returns resource-relative and publication-wide progression
// (both 0..1) for a plain-text byte offset within the resource identified by
// its caller-provided reading-order index. Invalid coordinates clamp safely.
func (c *Corpus) ProgressionsAt(resourceIndex int, offset int64) (float64, float64) {
	position, ok := c.resourcePosition[resourceIndex]
	if !ok || position < 0 || position >= len(c.resources) {
		return 0, 0
	}
	resourceLength := int64(len(c.resources[position].plain))
	if offset < 0 {
		offset = 0
	} else if offset > resourceLength {
		offset = resourceLength
	}

	resourceProgression := float64(0)
	if resourceLength > 0 {
		resourceProgression = float64(offset) / float64(resourceLength)
	}
	publicationProgression := float64(0)
	if c.totalPlain > 0 {
		publicationProgression = float64(c.cumulativePlain[position]+offset) / float64(c.totalPlain)
	}
	return resourceProgression, publicationProgression
}

// extractResource tokenizes raw markup into the three coordinate systems.
// Character references are decoded by the tokenizer, so NBSP arrives as a
// real rune and is never collapsed.
func extractResource(index int, href string, raw []byte) *resourceText {
	rt := &resourceText{index: index, href: href}

	var (
		plain    bytes.Buffer
		excluded int  // depth counter for excluded subtrees
		lastNL   bool // whether plain currently ends at a block boundary
	)

	// addSeparator records one normalized separator (' ') whose plain offset
	// is known immediately. Consecutive separators collapse.
	addSeparator := func(poff int64) {
		if len(rt.normRunes) > 0 && rt.normRunes[len(rt.normRunes)-1] == ' ' {
			return
		}
		rt.normRunes = append(rt.normRunes, ' ')
		rt.plainOff = append(rt.plainOff, poff)
		ni := int32(len(rt.normRunes) - 1)
		rt.foldRunes = append(rt.foldRunes, ' ')
		rt.srcIdx = append(rt.srcIdx, ni)
	}

	// ensureBoundary inserts a block boundary in both coordinate systems.
	ensureBoundary := func() {
		if plain.Len() > 0 && !lastNL {
			plain.WriteByte('\n')
			lastNL = true
		}
		if plain.Len() > 0 {
			addSeparator(int64(plain.Len() - 1))
		}
	}

	// emitSegment appends one non-space NFC segment.
	emitSegment := func(seg []byte, segPlainBase int64) {
		if len(seg) == 0 {
			return
		}
		var nfcStr string
		if norm.NFC.IsNormalString(string(seg)) {
			nfcStr = string(seg)
		} else {
			nfcStr = norm.NFC.String(string(seg))
		}

		for bi := 0; bi < len(nfcStr); {
			r, size := utf8.DecodeRuneInString(nfcStr[bi:])
			if size == 0 {
				size = 1
			}
			poff := segPlainBase + approximateSourceOffset(string(seg), nfcStr, bi)
			rt.normRunes = append(rt.normRunes, r)
			rt.plainOff = append(rt.plainOff, poff)
			ni := int32(len(rt.normRunes) - 1)
			for _, fr := range foldRune(r) {
				rt.foldRunes = append(rt.foldRunes, fr)
				rt.srcIdx = append(rt.srcIdx, ni)
			}
			bi += size
		}
		lastNL = false
	}

	z := html.NewTokenizer(bytes.NewReader(raw))
	for {
		switch tt := z.Next(); tt {
		case html.ErrorToken:
			if len(rt.normRunes) == 0 {
				return nil
			}
			rt.plainOff = append(rt.plainOff, int64(plain.Len()))
			rt.plain = plain.Bytes()
			return rt

		case html.TextToken:
			if excluded > 0 {
				continue
			}
			text := z.Text()
			if len(text) == 0 {
				continue
			}
			base := int64(plain.Len())
			plain.Write(text)
			// Split the token into non-space segments and ASCII space runs.
			i, n := 0, len(text)
			for i < n {
				j := i
				for j < n && !isASCIISpace(text[j]) {
					j++
				}
				if j > i {
					emitSegment(text[i:j], base+int64(i))
				}
				k := j
				for k < n && isASCIISpace(text[k]) {
					k++
				}
				if k > j && plain.Len() > 0 {
					// The collapsed separator points at the first space byte
					// in plain coordinates.
					addSeparator(base + int64(j))
				}
				i = k
			}

		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			ename := string(name)
			if excludedElements[ename] {
				excluded++
			} else if blockElements[ename] {
				ensureBoundary()
			}

		case html.EndTagToken:
			name, _ := z.TagName()
			ename := string(name)
			if excludedElements[ename] && excluded > 0 {
				excluded--
			} else if blockElements[ename] {
				ensureBoundary()
			}
		}
	}
}

// approximateSourceOffset maps byte offset bi inside the NFC form nfc of a
// source segment back to an offset inside seg. Exact when lengths match;
// otherwise interpolated by rune proportion.
func approximateSourceOffset(seg, nfc string, bi int) int64 {
	if len(seg) == len(nfc) {
		return int64(bi)
	}
	runesBefore := utf8.RuneCountInString(nfc[:bi])
	totalSrc := utf8.RuneCountInString(seg)
	if totalSrc == 0 {
		return 0
	}
	totalDst := utf8.RuneCountInString(nfc)
	if totalDst == 0 {
		return 0
	}
	return int64(runesBefore * len(seg) / totalDst)
}

// foldRune returns the full case-folded form of r. Most runes fold to a
// single rune; a handful expand.
func foldRune(r rune) []rune {
	switch r {
	case 'ß', 'ẛ':
		return []rune{'s', 's'}
	case 'ﬀ':
		return []rune{'f', 'f'}
	case 'ﬁ':
		return []rune{'f', 'i'}
	case 'ﬂ':
		return []rune{'f', 'l'}
	case 'ﬃ':
		return []rune{'f', 'f', 'i'}
	case 'ﬄ':
		return []rune{'f', 'f', 'l'}
	case 'ﬅ', 'ﬆ':
		return []rune{'s', 't'}
	}
	return []rune{unicode.ToLower(r)}
}

// isWordRune reports whether r counts as part of a word for boundary checks.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’'
}
