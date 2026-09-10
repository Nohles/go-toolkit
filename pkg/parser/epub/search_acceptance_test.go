package epub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixturePath resolves the locator fixture EPUB: explicit env override first,
// then the sibling Reader checkout, which is its canonical home.
func fixturePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("READIUM_SEARCH_FIXTURE"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	sibling, err := filepath.Abs("../../../Reader/test/fixtures/search-locator-fixture/search-locator-fixture.epub")
	if err == nil {
		if _, err := os.Stat(sibling); err == nil {
			return sibling
		}
	}
	// Also try relative to the package directory (test working dir).
	pkgRel := "../../../../Reader/test/fixtures/search-locator-fixture/search-locator-fixture.epub"
	if _, err := os.Stat(pkgRel); err == nil {
		return pkgRel
	}
	return ""
}

func openFixture(t *testing.T) *pub.Publication {
	t.Helper()
	path := fixturePath(t)
	if path == "" {
		t.Skip("fixture EPUB not found; set READIUM_SEARCH_FIXTURE or build Reader/test/fixtures/search-locator-fixture")
	}
	f, err := fetcher.NewArchiveFetcherFromPath(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(f.Close)

	builder, err := NewParser(nil).Parse(context.Background(), fakeEPUBAsset{name: filepath.Base(path)}, f)
	require.NoError(t, err)
	return builder.Build()
}

// TestFixtureSearchAcceptance walks every pathology of the locator fixture
// and asserts correct occurrence counts, original-casing highlights, and
// navigation-ready text context. Exit criterion from the map: every result
// decorates the correct occurrence.
func TestFixtureSearchAcceptance(t *testing.T) {
	p := openFixture(t)
	defer p.Close()

	require.True(t, p.IsSearchable(), "fixture must advertise a search service")
	opts, ok := p.SearchOptions()
	require.True(t, ok)
	assert.Equal(t, pub.DefaultSearchPageLength, opts.PageLength)

	ctx := context.Background()

	t.Run("repeated term across chapters", func(t *testing.T) {
		it, err := p.Search(ctx, "lantern")
		require.NoError(t, err)
		defer it.Close()
		total, known := it.Total()
		assert.True(t, known)
		assert.Equal(t, 15, total)

		var highlights []string
		for {
			page, err := it.Next(ctx)
			if err != nil {
				break
			}
			for _, loc := range page.Locators {
				assert.NotEmpty(t, loc.Text.Highlight)
				assert.Equal(t, mediatype.XHTML.String(), loc.MediaType.String())
				highlights = append(highlights, strings.ToLower(loc.Text.Highlight))
			}
			assert.LessOrEqual(t, len(page.Locators), pub.DefaultSearchPageLength)
		}
		assert.Len(t, highlights, 15)
	})

	t.Run("phrase split across inline elements", func(t *testing.T) {
		it, err := p.Search(ctx, "ivory compass")
		require.NoError(t, err)
		defer it.Close()
		count := collectCount(t, ctx, it)
		assert.GreaterOrEqual(t, count, 3, "all three inline variants must match")
	})

	t.Run("whitespace runs inside phrase", func(t *testing.T) {
		it, err := p.Search(ctx, "the ivory compass could be repaired")
		require.NoError(t, err)
		defer it.Close()
		assert.GreaterOrEqual(t, collectCount(t, ctx, it), 1)
	})

	t.Run("emoji single codepoints and ZWJ", func(t *testing.T) {
		for _, q := range []string{"⛵", "🚵", "🐦", "⛰", "⭐", "✨", "\U0001F635‍\U0001F4AB"} {
			it, err := p.Search(ctx, q)
			require.NoError(t, err, "query %q", q)
			count := collectCount(t, ctx, it)
			assert.GreaterOrEqual(t, count, 1, "query %q must match its chapter text", q)
		}
	})

	t.Run("combining characters and NFC forms", func(t *testing.T) {
		it, err := p.Search(ctx, "Na\u0308ynmo") // Decomposed spelling as authored
		require.NoError(t, err)
		assert.GreaterOrEqual(t, collectCount(t, ctx, it), 1)

		it2, err := p.Search(ctx, "N\u00e4ynmo") // NFC query against decomposed text
		require.NoError(t, err)
		assert.GreaterOrEqual(t, collectCount(t, ctx, it2), 1)

		it3, err := p.Search(ctx, "Björn") // Precomposed in ch06
		require.NoError(t, err)
		assert.Equal(t, 2, collectCount(t, ctx, it3))
	})

	t.Run("NBSP is content not separator", func(t *testing.T) {
		it, err := p.Search(ctx, "em dash")
		require.NoError(t, err)
		assert.Equal(t, 0, collectCount(t, ctx, it), "regular space must not match NBSP")

		it2, err := p.Search(ctx, "em\u00a0dash")
		require.NoError(t, err)
		assert.Equal(t, 1, collectCount(t, ctx, it2), "NBSP query matches")
	})

	t.Run("identical contexts disambiguated by position", func(t *testing.T) {
		it, err := p.Search(ctx, "bronze mirror")
		require.NoError(t, err)
		defer it.Close()
		total, _ := it.Total()
		assert.Equal(t, 4, total)

		var seen []string
		var progressions []float64
		for {
			page, err := it.Next(ctx)
			if err != nil {
				break
			}
			for _, loc := range page.Locators {
				assert.Equal(t, "bronze mirror", strings.ToLower(loc.Text.Highlight))
				assert.NotEmpty(t, loc.Text.Before)
				assert.NotEmpty(t, loc.Text.After)
				require.NotNil(t, loc.Locations.Progression)
				require.NotNil(t, loc.Locations.TotalProgression)
				seen = append(seen, loc.Text.Before+"|"+loc.Text.After)
				progressions = append(progressions, *loc.Locations.Progression)
			}
		}
		// Deterministic document order: each occurrence's context differs.
		assert.Len(t, seen, 4)
		assert.Len(t, uniqueFloat64s(progressions), 4,
			"each occurrence in one resource needs a distinct progression")
		assert.True(t, strings.Contains(seen[0], "artifact as a"),
			"first match is the catalog listing, got %q", seen[0])
	})

	t.Run("entity-decoded text is searchable", func(t *testing.T) {
		it, err := p.Search(ctx, "printer’s apprentice")
		require.NoError(t, err)
		assert.Equal(t, 1, collectCount(t, ctx, it))

		it2, err := p.Search(ctx, "cast")
		require.NoError(t, err)
		assert.GreaterOrEqual(t, collectCount(t, ctx, it2), 1)
	})
}

func uniqueFloat64s(values []float64) map[float64]struct{} {
	unique := make(map[float64]struct{}, len(values))
	for _, value := range values {
		unique[value] = struct{}{}
	}
	return unique
}

func collectCount(t *testing.T, ctx context.Context, it pub.SearchIterator) int {
	t.Helper()
	n := 0
	for {
		page, err := it.Next(ctx)
		if err != nil {
			return n
		}
		n += len(page.Locators)
	}
}

// TestFixtureCancellationRetriable proves a cancelled first search does not
// poison the publication for later searches.
func TestFixtureCancellationRetriable(t *testing.T) {
	p := openFixture(t)
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Search(ctx, "lantern")
	assert.ErrorIs(t, err, context.Canceled, "cancelled build reports cancellation")

	// Fresh context rebuilds the corpus and succeeds.
	it, err := p.Search(context.Background(), "lantern")
	require.NoError(t, err)
	defer it.Close()
	total, _ := it.Total()
	assert.Equal(t, 15, total)
}

// TestFixturePagination exercises iterator paging over the repeated-term
// chapter with a small page size.
func TestFixturePagination(t *testing.T) {
	p := openFixture(t)
	defer p.Close()

	svc, ok := p.SearchService()
	require.True(t, ok)
	it, err := svc.Search(context.Background(), "lantern")
	require.NoError(t, err)
	defer it.Close()

	pages := 0
	seen := 0
	for {
		page, err := it.Next(context.Background())
		if err != nil {
			break
		}
		pages++
		seen += len(page.Locators)
		assert.LessOrEqual(t, len(page.Locators), pub.DefaultSearchPageLength)
		if !page.HasNext {
			break
		}
	}
	assert.Equal(t, 15, seen)
	assert.Equal(t, 1, pages) // 15 < page length 20 → single page
}

// TestQueryValidation covers invalid-query errors.
func TestQueryValidation(t *testing.T) {
	p := openFixture(t)
	defer p.Close()

	_, err := p.Search(context.Background(), "   ")
	assert.ErrorIs(t, err, pub.ErrEmptyQuery)

	_, err = p.Search(context.Background(), strings.Repeat("x", 500))
	assert.ErrorIs(t, err, pub.ErrQueryTooLong)
}
