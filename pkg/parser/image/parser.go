package image

import (
	"context"
	"errors"
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
)

// Parses an image–based Publication from an unstructured archive format containing bitmap files, such as CBZ or a simple ZIP.
// It can also work for a standalone bitmap file.
type ImageParser struct{}

func NewParser() ImageParser {
	return ImageParser{}
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

	sort.Slice(readingOrder, func(i, j int) bool {
		return naturalLess(readingOrder[i].Href.String(), readingOrder[j].Href.String())
	})
	return readingOrder, true
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
