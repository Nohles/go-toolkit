package image

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nohles/go-toolkit/pkg/asset"
	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/internal/extensions"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/parser"
	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/nohles/go-toolkit/pkg/util/url"
)

// Parses an image–based Publication from an unstructured archive format containing bitmap files, such as CBZ or a simple ZIP.
// It can also work for a standalone bitmap file.
type ImageParser struct {
	comicArchiveReadingOrder []manifest.HREF
}

type Option func(*ImageParser)

// WithComicArchiveReadingOrder restricts a directory-of-archives publication
// to the given ordered HREFs. The HREFs must already exist in the directory.
func WithComicArchiveReadingOrder(hrefs ...manifest.HREF) Option {
	return func(parser *ImageParser) {
		parser.comicArchiveReadingOrder = make([]manifest.HREF, len(hrefs))
		copy(parser.comicArchiveReadingOrder, hrefs)
	}
}

func NewParser(options ...Option) ImageParser {
	parser := ImageParser{}
	for _, option := range options {
		option(&parser)
	}
	return parser
}

// Parse implements PublicationParser
func (p ImageParser) Parse(ctx context.Context, asset asset.PublicationAsset, fetcher fetcher.Fetcher) (*pub.Builder, error) {
	links, err := fetcher.Links(ctx)
	if err != nil {
		return nil, err
	}

	if asset.MediaType(ctx).IsComicArchive() {
		return p.parseImagePublication(ctx, asset, fetcher, links)
	}

	if readingOrder, ok := comicArchiveReadingOrder(links); ok {
		if p.comicArchiveReadingOrder != nil {
			readingOrder, err = selectComicArchiveReadingOrder(readingOrder, p.comicArchiveReadingOrder)
			if err != nil {
				return nil, err
			}
		}
		return p.parseComicArchivePublication(ctx, asset, fetcher, readingOrder), nil
	}

	if !acceptsImageLinks(links) {
		return nil, nil
	}

	return p.parseImagePublication(ctx, asset, fetcher, links)
}

func (p ImageParser) parseImagePublication(ctx context.Context, asset asset.PublicationAsset, fetcher fetcher.Fetcher, links manifest.LinkList) (*pub.Builder, error) {
	readingOrder := make(manifest.LinkList, 0, len(links))
	for _, link := range links {
		path := link.URL(nil, nil).Path()

		// Filter out all irrelevant files
		if extensions.IsHiddenOrThumbs(path) || link.MediaType == nil || !link.MediaType.IsBitmap() {
			continue
		}
		readingOrder = append(readingOrder, link)
	}

	if len(readingOrder) == 0 {
		return nil, errors.New("no bitmap found in the publication")
	}

	// Sort in alphabetical order
	sort.Slice(readingOrder, func(i, j int) bool {
		return readingOrder[i].Href.String() < readingOrder[j].Href.String()
	})

	// Try to figure out the publication's title
	title := parser.GuessPublicationTitleFromFileStructure(ctx, fetcher)
	if title == "" {
		title = asset.Name()
	}

	// First valid resource is the cover.
	readingOrder[0].Rels = []string{"cover"}

	manifest := manifest.Manifest{
		Context: manifest.Strings{manifest.WebpubManifestContext},
		Metadata: manifest.Metadata{
			LocalizedTitle: manifest.NewLocalizedStringFromString(title),
			ConformsTo:     manifest.Profiles{manifest.ProfileDivina},
		},
		ReadingOrder: readingOrder,
	}

	builder := pub.NewServicesBuilder(map[pub.ServiceName]pub.ServiceFactory{
		pub.PositionsService_Name: pub.PerResourcePositionsServiceFactory(mediatype.MustNewOfString("image/*")),
	})
	return pub.NewBuilder(manifest, fetcher, builder), nil
}

var allowed_extensions_image = map[string]struct{}{"acbf": {}, "xml": {}, "txt": {}, "json": {}}

func (p ImageParser) accepts(ctx context.Context, asset asset.PublicationAsset, fetcher fetcher.Fetcher) (bool, error) {
	if asset.MediaType(ctx).IsComicArchive() {
		return true, nil
	}
	links, err := fetcher.Links(ctx)
	if err != nil {
		return false, err
	}
	if _, ok := comicArchiveReadingOrder(links); ok {
		return true, nil
	}
	return acceptsImageLinks(links), nil
}

func acceptsImageLinks(links manifest.LinkList) bool {
	for _, link := range links {
		path := link.URL(nil, nil).Path()

		if extensions.IsHiddenOrThumbs(path) {
			continue
		}
		if link.MediaType != nil && link.MediaType.IsBitmap() {
			continue
		}
		fext := filepath.Ext(strings.ToLower(path))
		if len(fext) > 1 {
			fext = fext[1:] // Remove "." from extension
		}
		_, contains := allowed_extensions_image[fext]
		if !contains {
			return false
		}
	}
	return true
}

func (p ImageParser) parseComicArchivePublication(ctx context.Context, asset asset.PublicationAsset, fetcher fetcher.Fetcher, readingOrder manifest.LinkList) *pub.Builder {
	title := parser.GuessPublicationTitleFromFileStructure(ctx, fetcher)
	if title == "" {
		title = asset.Name()
	}

	manifest := manifest.Manifest{
		Context: manifest.Strings{manifest.WebpubManifestContext},
		Metadata: manifest.Metadata{
			LocalizedTitle: manifest.NewLocalizedStringFromString(title),
			ConformsTo:     manifest.Profiles{manifest.ProfileDivina},
		},
		ReadingOrder:    readingOrder,
		TableOfContents: comicArchiveTableOfContents(readingOrder),
	}

	builder := pub.NewServicesBuilder(map[pub.ServiceName]pub.ServiceFactory{
		pub.PositionsService_Name: pub.PerResourcePositionsServiceFactory(mediatype.CBZ),
	})
	return pub.NewBuilder(manifest, fetcher, builder)
}

func comicArchiveReadingOrder(links manifest.LinkList) (manifest.LinkList, bool) {
	readingOrder := make(manifest.LinkList, 0, len(links))
	for _, link := range links {
		path := link.URL(nil, nil).Path()

		if extensions.IsHiddenOrThumbs(path) {
			continue
		}

		mt := link.MediaType
		if mt == nil || mt.Equal(&mediatype.Binary) {
			mt = mediaTypeForPath(path)
		}
		if mt != nil && mt.IsComicArchive() {
			link.Href = relativePublicationHREF(link.Href)
			link.MediaType = mt
			readingOrder = append(readingOrder, link)
			continue
		}

		fext := filepath.Ext(strings.ToLower(path))
		if len(fext) > 1 {
			fext = fext[1:]
		}
		_, contains := allowed_extensions_image[fext]
		if !contains {
			return nil, false
		}
	}
	if len(readingOrder) == 0 {
		return nil, false
	}

	sort.SliceStable(readingOrder, func(i, j int) bool {
		leftOrder, leftHasOrder := chapterOrderNumber(titleFromComicArchiveHref(readingOrder[i]))
		rightOrder, rightHasOrder := chapterOrderNumber(titleFromComicArchiveHref(readingOrder[j]))
		if leftHasOrder && rightHasOrder && leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		if leftHasOrder != rightHasOrder {
			return leftHasOrder
		}
		return naturalLess(readingOrder[i].Href.String(), readingOrder[j].Href.String())
	})
	return readingOrder, true
}

func canonicalComicArchiveHREF(href manifest.HREF) (string, error) {
	if href.IsTemplated() {
		return "", errors.New("comic archive selection HREF must not be templated")
	}
	u := href.Resolve(nil, nil)
	raw := u.Raw()
	decodedPath := u.Path()
	if raw.IsAbs() || raw.Host != "" || raw.RawQuery != "" || raw.ForceQuery || raw.Fragment != "" {
		return "", fmt.Errorf("comic archive selection HREF %q must be a relative path without query or fragment", href.String())
	}
	if decodedPath == "" || strings.HasPrefix(decodedPath, "/") || strings.Contains(decodedPath, "\\") || strings.Contains(decodedPath, "//") {
		return "", fmt.Errorf("comic archive selection HREF %q is not a normalized relative path", href.String())
	}
	cleaned := pathpkg.Clean(decodedPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != decodedPath {
		return "", fmt.Errorf("comic archive selection HREF %q contains invalid path traversal", href.String())
	}
	canonical, err := url.URLFromDecodedPath(decodedPath)
	if err != nil {
		return "", fmt.Errorf("failed normalizing comic archive selection HREF %q: %w", href.String(), err)
	}
	return canonical.String(), nil
}

func selectComicArchiveReadingOrder(discovered manifest.LinkList, requested []manifest.HREF) (manifest.LinkList, error) {
	if len(requested) == 0 {
		return nil, errors.New("comic archive reading order selection must not be empty")
	}

	byHREF := make(map[string]manifest.Link, len(discovered))
	for _, link := range discovered {
		canonical, err := canonicalComicArchiveHREF(link.Href)
		if err != nil {
			return nil, err
		}
		byHREF[canonical] = link
	}

	selected := make(manifest.LinkList, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, href := range requested {
		canonical, err := canonicalComicArchiveHREF(href)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[canonical]; duplicate {
			return nil, fmt.Errorf("comic archive reading order selection contains duplicate HREF %q", canonical)
		}
		link, exists := byHREF[canonical]
		if !exists {
			return nil, fmt.Errorf("comic archive reading order selection contains unknown HREF %q", canonical)
		}
		seen[canonical] = struct{}{}
		selected = append(selected, link)
	}
	return selected, nil
}

func comicArchiveTableOfContents(readingOrder manifest.LinkList) manifest.LinkList {
	toc := make(manifest.LinkList, len(readingOrder))
	for i, link := range readingOrder {
		toc[i] = manifest.Link{
			Href:      link.Href,
			MediaType: link.MediaType,
			Title:     titleFromComicArchiveHref(link),
		}
	}
	return toc
}

func mediaTypeForPath(path string) *mediatype.MediaType {
	fext := filepath.Ext(strings.ToLower(path))
	if len(fext) <= 1 {
		return nil
	}
	return mediatype.OfExtension(fext[1:])
}

func relativePublicationHREF(href manifest.HREF) manifest.HREF {
	return manifest.MustNewHREFFromString(strings.TrimPrefix(href.String(), "/"), href.IsTemplated())
}

func titleFromComicArchiveHref(link manifest.Link) string {
	title := link.URL(nil, nil).Filename()
	ext := filepath.Ext(title)
	if ext != "" {
		title = strings.TrimSuffix(title, ext)
	}
	return strings.TrimSpace(title)
}

var naturalNumberPattern = regexp.MustCompile(`\d+`)

var chapterOrderPatterns = []struct {
	pattern *regexp.Regexp
	group   int
}{
	{regexp.MustCompile(`(?i)(^|[\s_.-])(chapter|ch)\s*\.?\s*([0-9]+(\.[0-9]+)?)\b`), 3},
	{regexp.MustCompile(`(?i)(^|[\s_.-])c([0-9]+(\.[0-9]+)?)\b`), 2},
	{regexp.MustCompile(`(?i)(^|[\s_.-])(episode|episodes|ep)\s*\.?\s*([0-9]+(\.[0-9]+)?)\b`), 3},
	{regexp.MustCompile(`(^|[\s_.-])#\s*\.?\s*([0-9]+(\.[0-9]+)?)\b`), 2},
	{regexp.MustCompile(`(?i)(vol|volume|v)\s*\.?\s*([0-9]+(\.[0-9]+)?)`), 2},
	{regexp.MustCompile(`第([0-9]+(\.[0-9]+)?)話`), 1},
	{regexp.MustCompile(`第([0-9]+)巻`), 1},
}

var specialOrderPattern = regexp.MustCompile(`(?i)\bSP\s*[0-9]+(\.[0-9]+)?\b`)

func chapterOrderNumber(filename string) (float64, bool) {
	filename = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ").Replace(filename)
	filename = strings.Join(strings.Fields(filename), " ")
	if specialOrderPattern.MatchString(filename) {
		return 0, false
	}

	for _, candidate := range chapterOrderPatterns {
		match := candidate.pattern.FindStringSubmatch(filename)
		if len(match) <= candidate.group {
			continue
		}
		number, err := strconv.ParseFloat(match[candidate.group], 64)
		if err == nil && number >= 0 && number <= 9999 {
			return number, true
		}
	}

	if number, err := strconv.ParseFloat(filename, 64); err == nil && number >= 0 && number <= 9999 {
		return number, true
	}
	return 0, false
}

func naturalLess(left string, right string) bool {
	leftNumbers := naturalNumberPattern.FindAllStringIndex(left, -1)
	rightNumbers := naturalNumberPattern.FindAllStringIndex(right, -1)

	for i := 0; i < len(leftNumbers) && i < len(rightNumbers); i++ {
		leftRange := leftNumbers[i]
		rightRange := rightNumbers[i]
		leftPrefix := left[:leftRange[0]]
		rightPrefix := right[:rightRange[0]]
		if leftPrefix != rightPrefix {
			return left < right
		}
		leftValue, leftErr := strconv.Atoi(left[leftRange[0]:leftRange[1]])
		rightValue, rightErr := strconv.Atoi(right[rightRange[0]:rightRange[1]])
		if leftErr != nil || rightErr != nil || leftValue == rightValue {
			continue
		}
		return leftValue < rightValue
	}

	return left < right
}
