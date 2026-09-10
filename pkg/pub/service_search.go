package pub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/search"
	"github.com/nohles/go-toolkit/pkg/util/url"
)

// SearchLink is the well-known link exposing the search service of a
// publication. The URI template carries the mandatory query parameter.
var SearchLink = manifest.Link{
	Href:      manifest.MustNewHREFFromString("~readium/search{?query}", true),
	MediaType: &mediatype.ReadiumLocatorsJSON,
	Rels:      manifest.Strings{"search"},
}

// SearchOptions describes the capabilities of a search service.
type SearchOptions struct {
	// PageLength is the fixed number of results per page, or 0 when the
	// implementation does not paginate.
	PageLength int
}

// SearchService implements [Service]: it searches a publication's reading
// order and returns results as paginated locator collections.
type SearchService interface {
	Service
	// Options reports supported search options.
	Options() SearchOptions
	// Search runs a query. Invalid queries return an error rather than empty
	// results; cancellation is reported through ctx and the returned error.
	Search(ctx context.Context, query string) (SearchIterator, error)
}

// SearchIterator yields pages of search results.
type SearchIterator interface {
	// Total returns the number of results across all pages, when known.
	// The second return value reports whether the count is available.
	Total() (int, bool)
	// Next returns the next page. It returns [ErrIteratorDone] when
	// exhausted.
	Next(context.Context) (LocatorCollectionPage, error)
	// Close releases resources held by the iteration.
	Close()
}

// ErrIteratorDone reports that the iterator has no more pages.
var ErrIteratorDone error = io.EOF

// LocatorCollectionPage is one page of search results.
type LocatorCollectionPage struct {
	Locators []manifest.Locator
	// Number is the 1-based index of this page.
	Number int
	// HasNext reports whether another page follows.
	HasNext bool
}

// Default limits for the corpus-backed search service.
const (
	DefaultSearchPageLength = 20
	DefaultMaxTotalResults  = 1000
	DefaultMaxQueryRunes    = 200
	DefaultSearchExtractCap = int64(2 << 20)
)

// Sentinel errors distinguishing failure modes for callers.
var (
	ErrEmptyQuery   = errors.New("search: empty query")
	ErrQueryTooLong = errors.New("search: query exceeds maximum length")
)

// CorpusSearchService implements [SearchService] over a lazily built
// [search.Corpus]. The corpus is built once per service instance on first
// use; concurrent first searches share one build. A failed build (including
// cancellation) is not sticky: a later call with a live context retries.
type CorpusSearchService struct {
	public       bool
	readingOrder manifest.LinkList
	fetcher      fetcher.Fetcher
	toc          manifest.LinkList

	engine        search.Engine
	pageLength    int
	maxTotal      int
	maxQueryRunes int
	extractLimit  int64

	tocTitlesOnce sync.Once
	tocTitles     map[string]string // resource href -> TOC title

	mu     sync.Mutex
	corpus *search.Corpus
}

// CorpusSearchOption configures a [CorpusSearchService].
type CorpusSearchOption func(*CorpusSearchService)

// WithSearchEngine replaces the matching engine.
func WithSearchEngine(e search.Engine) CorpusSearchOption {
	return func(s *CorpusSearchService) { s.engine = e }
}

// WithSearchLimits overrides the default caps. Zero values keep defaults.
func WithSearchLimits(pageLength, maxTotal, maxQueryRunes int, extractLimitBytes int64) CorpusSearchOption {
	return func(s *CorpusSearchService) {
		if pageLength > 0 {
			s.pageLength = pageLength
		}
		if maxTotal > 0 {
			s.maxTotal = maxTotal
		}
		if maxQueryRunes > 0 {
			s.maxQueryRunes = maxQueryRunes
		}
		if extractLimitBytes > 0 {
			s.extractLimit = extractLimitBytes
		}
	}
}

// NewCorpusSearchService creates a search service over the given reading
// order and fetcher.
// NewCorpusSearchService creates a search service over the given reading
// order, fetcher, and table of contents (for result titles).
func NewCorpusSearchService(readingOrder manifest.LinkList, f fetcher.Fetcher, toc manifest.LinkList, opts ...CorpusSearchOption) *CorpusSearchService {
	s := &CorpusSearchService{
		readingOrder:  readingOrder,
		fetcher:       f,
		toc:           toc,
		engine:        search.NewLiteralEngine(),
		pageLength:    DefaultSearchPageLength,
		maxTotal:      DefaultMaxTotalResults,
		maxQueryRunes: DefaultMaxQueryRunes,
		extractLimit:  DefaultSearchExtractCap,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *CorpusSearchService) Close() {}

// SetPublic toggles whether the service exposes its link in manifests and
// serves GETs on it.
func (s *CorpusSearchService) SetPublic(public bool) { s.public = public }

func (s *CorpusSearchService) Links() manifest.LinkList {
	if !s.public {
		return nil
	}
	return manifest.LinkList{SearchLink}
}

func (s *CorpusSearchService) Get(ctx context.Context, link manifest.Link) (fetcher.Resource, bool) {
	if !s.public {
		return nil, false
	}
	return GetForSearchService(ctx, s, link)
}

// Options implements [SearchService].
func (s *CorpusSearchService) Options() SearchOptions {
	return SearchOptions{PageLength: s.pageLength}
}

// Search implements [SearchService].
func (s *CorpusSearchService) Search(ctx context.Context, query string) (SearchIterator, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}
	if len([]rune(query)) > s.maxQueryRunes {
		return nil, ErrQueryTooLong
	}

	corpus, err := s.ensureCorpus(ctx)
	if err != nil {
		return nil, err
	}

	all, err := s.engine.Find(ctx, corpus, query)
	if err != nil {
		return nil, err
	}
	truncated := false
	if len(all) > s.maxTotal {
		all = all[:s.maxTotal]
		truncated = true
	}

	titleFor := func(href string) string {
		s.tocTitlesOnce.Do(func() {
			s.tocTitles = buildTOCTitles(s.toc)
		})
		return s.tocTitles[stripFragment(href)]
	}

	return &sliceIterator{
		matches:   all,
		pageLen:   s.pageLength,
		total:     len(all),
		truncated: truncated,
		hrefs:     hrefsOf(searchableHrefs(s.readingOrder)),
		titleFor:  titleFor,
		corpus:    corpus,
	}, nil
}

// searchableHrefs lists the hrefs of searchable resources in corpus order.
func searchableHrefs(ro manifest.LinkList) []string {
	searchable := SearchableReadingOrder(ro)
	hrefs := make([]string, len(searchable))
	for i, l := range searchable {
		hrefs[i] = l.Href.String()
	}
	return hrefs
}

// hrefsOf is an identity helper keeping sliceIterator's href table explicit.
func hrefsOf(hrefs []string) []string { return hrefs }

// buildTOCTitles maps resource hrefs to their most specific TOC title by
// walking the TOC tree; children inherit the deepest ancestor title.
func buildTOCTitles(toc manifest.LinkList) map[string]string {
	titles := make(map[string]string)
	var walk func(links manifest.LinkList, inherited string)
	walk = func(links manifest.LinkList, inherited string) {
		for _, l := range links {
			title := l.Title
			if title == "" {
				title = inherited
			}
			if title != "" && l.Href.String() != "" {
				base := stripFragment(l.Href.String())
				if _, exists := titles[base]; !exists {
					titles[base] = title
				}
			}
			walk(l.Children, title)
		}
	}
	walk(toc, "")
	return titles
}

// stripFragment removes any #fragment from an href string.
func stripFragment(href string) string {
	if i := strings.IndexByte(href, '#'); i >= 0 {
		return href[:i]
	}
	return href
}

// ensureCorpus builds the corpus on first use, serialized by mutex. Failures
// are not sticky; the next call retries.
func (s *CorpusSearchService) ensureCorpus(ctx context.Context) (*search.Corpus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.corpus != nil {
		return s.corpus, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	searchable := SearchableReadingOrder(s.readingOrder)
	resources := make([]search.Resource, len(searchable))
	for i, link := range searchable {
		resources[i] = search.Resource{Index: i, Href: link.Href.String()}
	}

	ext := search.Extractor{MaxResourceBytes: s.extractLimit}
	for i, link := range searchable {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		res := s.fetcher.Get(ctx, link)
		data, rerr := readAllLimited(ctx, res, s.extractLimit)
		res.Close()
		if rerr != nil {
			continue // Broken resources are skipped individually.
		}
		resources[i] = search.Resource{Index: i, Href: link.Href.String(), Bytes: data}
	}

	corpus, berr := ext.Build(ctx, resources)
	if berr != nil {
		return nil, berr
	}
	s.corpus = corpus
	return corpus, nil
}

// SearchableReadingOrder filters a reading order down to resources whose
// media types are searchable by this package's extractor.
func SearchableReadingOrder(ro manifest.LinkList) manifest.LinkList {
	out := make(manifest.LinkList, 0, len(ro))
	for _, l := range ro {
		mt := "application/xhtml+xml"
		if l.MediaType != nil {
			mt = l.MediaType.String()
		}
		switch mt {
		case "application/xhtml+xml", "text/html", "text/plain", "application/xml":
			out = append(out, l)
		}
	}
	return out
}

// readAllLimited reads a resource fully, refusing to buffer more than limit
// bytes.
func readAllLimited(ctx context.Context, res fetcher.Resource, limit int64) ([]byte, error) {
	if res == nil {
		return nil, errors.New("search: nil resource")
	}
	if length, err := res.Length(ctx); err == nil && length > limit {
		return nil, errors.New("search: resource exceeds extraction limit")
	}
	data, rerr := res.Read(ctx, 0, 0)
	if rerr != nil {
		return nil, rerr
	}
	if int64(len(data)) > limit {
		return nil, errors.New("search: resource exceeds extraction limit")
	}
	return data, nil
}

// sliceIterator paginates a materialized match slice into locator pages.
type sliceIterator struct {
	matches   []search.Match
	pageLen   int
	total     int
	truncated bool
	pos       int

	hrefs    []string // corpus index -> resource href
	titleFor func(string) string
	corpus   *search.Corpus
}

func (it *sliceIterator) Total() (int, bool) { return it.total, !it.truncated || it.total > 0 }

func (it *sliceIterator) Close() {}

func (it *sliceIterator) Next(ctx context.Context) (LocatorCollectionPage, error) {
	if err := ctx.Err(); err != nil {
		return LocatorCollectionPage{}, err
	}
	if it.pos >= len(it.matches) {
		return LocatorCollectionPage{}, ErrIteratorDone
	}
	end := it.pos + it.pageLen
	if end > len(it.matches) {
		end = len(it.matches)
	}
	pageMatches := it.matches[it.pos:end]
	number := it.pos/it.pageLen + 1
	it.pos = end

	locators := make([]manifest.Locator, len(pageMatches))
	for i, m := range pageMatches {
		locators[i] = it.locator(m)
	}
	return LocatorCollectionPage{
		Locators: locators,
		Number:   number,
		HasNext:  it.pos < len(it.matches),
	}, nil
}

// locator converts one engine match into a Readium locator.
func (it *sliceIterator) locator(m search.Match) manifest.Locator {
	href := ""
	if m.ResourceIndex >= 0 && m.ResourceIndex < len(it.hrefs) {
		href = it.hrefs[m.ResourceIndex]
	}
	progression, totalProgression := float64(0), float64(0)
	if it.corpus != nil {
		progression, totalProgression = it.corpus.ProgressionsAt(m.ResourceIndex, m.Start)
	}
	loc := manifest.Locator{
		Href:      locatorHref(href),
		MediaType: mediatype.XHTML,
		Title:     it.titleFor(href),
		Locations: manifest.Locations{
			Progression:      &progression,
			TotalProgression: &totalProgression,
		},
		Text: manifest.Text{
			Before:    m.Before,
			Highlight: m.Highlight,
			After:     m.After,
		},
	}
	return loc
}

// locatorHref parses a resource href string into a URL for locators.
func locatorHref(href string) url.URL {
	u, err := url.URLFromString(href)
	if err != nil {
		return nil
	}
	return u
}

// errIteratorDone was renamed to the exported [ErrIteratorDone].

// GetForSearchService materializes the generic GET response when the link is
// the search link without parameters. The CLI layer handles query-parameter
// expansion itself; this fallback serves a Problem Details document.
func GetForSearchService(ctx context.Context, service SearchService, link manifest.Link) (fetcher.Resource, bool) {
	if !link.URL(nil, nil).Equivalent(SearchLink.URL(nil, nil)) {
		return nil, false
	}
	return fetcher.NewBytesResource(SearchLink, func() []byte {
		bin, _ := json.Marshal(map[string]interface{}{
			"type":   "https://readium.org/publication-server/error/search-missing-query",
			"title":  "Missing query parameter",
			"status": 400,
		})
		return bin
	}), true
}

// Publication helpers -------------------------------------------------------

// IsSearchable reports whether the publication exposes a search service.
func (p Publication) IsSearchable() bool {
	_, ok := p.SearchService()
	return ok
}

// SearchOptions returns the publication's search options, if searchable.
func (p Publication) SearchOptions() (SearchOptions, bool) {
	ss, ok := p.SearchService()
	if !ok {
		return SearchOptions{}, false
	}
	return ss.Options(), true
}

// SearchService returns the publication's [SearchService], if any.
func (p Publication) SearchService() (SearchService, bool) {
	svc := p.FindService(SearchService_Name)
	if svc == nil {
		return nil, false
	}
	ss, ok := svc.(SearchService)
	return ss, ok
}

// Search runs a query against the publication's search service.
func (p Publication) Search(ctx context.Context, query string) (SearchIterator, error) {
	ss, ok := p.SearchService()
	if !ok {
		return nil, errors.New("publication is not searchable")
	}
	return ss.Search(ctx, query)
}
