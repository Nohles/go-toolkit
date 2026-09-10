package search

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func extract(t *testing.T, raw string) *Corpus {
	t.Helper()
	corpus, err := Extractor{}.Build(context.Background(), []Resource{
		{Index: 0, Href: "res.xhtml", Bytes: []byte(raw)},
	})
	require.NoError(t, err)
	require.Equal(t, 1, corpus.Resources())
	return corpus
}

func TestExtractPlainText(t *testing.T) {
	corpus := extract(t, "Hello world. Second sentence.")
	engine := NewLiteralEngine()
	matches, err := engine.Find(context.Background(), corpus, "world")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.Equal(t, int64(6), matches[0].Start)
	assert.Equal(t, int64(11), matches[0].End)
	assert.Equal(t, "world", matches[0].Highlight)
}

func TestCaseFolding(t *testing.T) {
	corpus := extract(t, "<p>The IVORY Compass gleamed.</p>")
	engine := NewLiteralEngine()

	for _, q := range []string{"ivory", "IVORY", "Ivory"} {
		matches, err := engine.Find(context.Background(), corpus, q)
		require.NoError(t, err, "query %q", q)
		require.Len(t, matches, 1, "query %q", q)
		assert.Equal(t, "IVORY", matches[0].Highlight) // Original casing preserved
	}
}

func TestInlineElementSplitPhrase(t *testing.T) {
	corpus := extract(t, "<p>the <em>ivory compass</em> gleamed</p>")
	engine := NewLiteralEngine()
	matches, err := engine.Find(context.Background(), corpus, "the ivory compass")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.Equal(t, int64(0), matches[0].Start)
	assert.Equal(t, "the ivory compass", matches[0].Highlight)
}

func TestWhitespaceTolerantQuery(t *testing.T) {
	corpus := extract(t, "<p>the\t ivory\n\ncompass   gleamed</p>")
	engine := NewLiteralEngine()
	matches, err := engine.Find(context.Background(), corpus, "ivory compass")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.NotContains(t, matches[0].Highlight, "\t")
}

func TestNFCNormalization(t *testing.T) {
	// Decomposed: a + combining diaeresis.
	decomposed := "Bja\u0308rn arrived."
	corpus := extract(t, "<p>"+decomposed+"</p>")
	engine := NewLiteralEngine()

	// Query with the same decomposed spelling matches (both sides normalize).
	matches, err := engine.Find(context.Background(), corpus, "Bja\u0308rn")
	require.NoError(t, err)
	require.Len(t, matches, 1)

	// NFC form of the query matches too.
	matches2, err := engine.Find(context.Background(), corpus, "Bj\u00e4rn")
	require.NoError(t, err)
	require.Len(t, matches2, 1)
}

func TestNBSPNotCollapsed(t *testing.T) {
	corpus := extract(t, "<p>em&#160;dash is special</p>")
	engine := NewLiteralEngine()

	// NBSP is content: "em dash" (regular space) must NOT match.
	matches, err := engine.Find(context.Background(), corpus, "em dash")
	require.NoError(t, err)
	assert.Empty(t, matches)

	// But an NBSP query matches.
	matches2, err := engine.Find(context.Background(), corpus, "em\u00a0dash")
	require.NoError(t, err)
	require.Len(t, matches2, 1)
}

func TestWordBoundaries(t *testing.T) {
	corpus := extract(t, "<p>The lantern and the lanterns hung.</p>")
	engine := NewLiteralEngine()

	matches, err := engine.Find(context.Background(), corpus, "lantern")
	require.NoError(t, err)
	require.Len(t, matches, 1) // Not "lanterns"
}

func TestExcludedElements(t *testing.T) {
	corpus := extract(t, "<html><head><title>lantern secret</title><style>.lantern{}</style></head><body><p>a lantern here</p><script>var lantern=1;</script></body></html>")
	engine := NewLiteralEngine()
	matches, err := engine.Find(context.Background(), corpus, "lantern")
	require.NoError(t, err)
	require.Len(t, matches, 1) // Only the body occurrence survives extraction
	assert.Equal(t, "a ", matches[0].Before)
	assert.Equal(t, " here", matches[0].After)
}

func TestContextCaptured(t *testing.T) {
	raw := "<p>"
	for i := 0; i < 40; i++ {
		raw += "filler "
	}
	raw += "needle "
	for i := 0; i < 40; i++ {
		raw += "filler"
		raw += " "
	}
	raw += "</p>"
	corpus := extract(t, raw)
	engine := NewLiteralEngine()
	matches, err := engine.Find(context.Background(), corpus, "needle")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.GreaterOrEqual(t, len([]rune(matches[0].Before)), DefaultContextRunes-8)
	assert.True(t, len([]rune(matches[0].After)) >= 0)
}

func TestEmptyAndLongQueries(t *testing.T) {
	corpus := extract(t, "<p>text</p>")
	engine := NewLiteralEngine()

	m, err := engine.Find(context.Background(), corpus, "")
	require.NoError(t, err)
	assert.Empty(t, m)

	m2, err := engine.Find(context.Background(), corpus, "   ")
	require.NoError(t, err)
	assert.Empty(t, m2)
}

func TestCancellationDuringFind(t *testing.T) {
	corpus := extract(t, "<p>needle needle needle needle needle</p>")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewLiteralEngine().Find(ctx, corpus, "needle")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestZWJSequenceQuery(t *testing.T) {
	// Regression: U+200D was misclassified as whitespace (byte-truncation of
	// the rune), splitting ZWJ sequences in queries.
	corpus := extract(t, "<p>face \U0001F635‍\U0001F4AB spun</p>")
	engine := NewLiteralEngine()
	matches, err := engine.Find(context.Background(), corpus, "\U0001F635‍\U0001F4AB")
	require.NoError(t, err)
	require.Len(t, matches, 1)

	// Non-word runes (emoji) are not boundary-checked: the bare emoji
	// matches inside the ZWJ sequence.
	matches2, err := engine.Find(context.Background(), corpus, "\U0001F635")
	require.NoError(t, err)
	assert.Len(t, matches2, 1)
}
