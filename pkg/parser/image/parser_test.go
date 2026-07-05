package image

import (
	"context"
	"testing"

	"github.com/nohles/go-toolkit/pkg/archive"
	"github.com/nohles/go-toolkit/pkg/asset"
	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/nohles/go-toolkit/pkg/util/url"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticLinksFetcher struct {
	fetcher.EmptyFetcher
	links manifest.LinkList
}

func (f staticLinksFetcher) Links(ctx context.Context) (manifest.LinkList, error) {
	return f.links, nil
}

type staticAsset struct {
	mediaType mediatype.MediaType
}

func (a staticAsset) Name() string {
	return "test"
}

func (a staticAsset) MediaType(ctx context.Context) mediatype.MediaType {
	return a.mediaType
}

func (a staticAsset) CreateFetcher(ctx context.Context, dependencies asset.Dependencies, credentials string) (fetcher.Fetcher, error) {
	return fetcher.EmptyFetcher{}, nil
}

func withImageParser(t *testing.T, filepath string, f func(*pub.Builder)) {
	u, _ := url.FromFilepath(filepath)
	a := asset.File(u)
	fet, err := a.CreateFetcher(t.Context(), asset.Dependencies{
		ArchiveFactory: archive.NewArchiveFactory(),
	}, "")
	require.NoError(t, err)
	p, err := ImageParser{}.Parse(t.Context(), a, fet)
	require.NoError(t, err)
	f(p)
}

func TestImageCBZAccepted(t *testing.T) {
	withImageParser(t, "./testdata/image/futuristic_tales.cbz", func(p *pub.Builder) {
		assert.NotNil(t, p)
	})
}

func TestImageJPGAccepted(t *testing.T) {
	withImageParser(t, "./testdata/image/futuristic_tales.jpg", func(p *pub.Builder) {
		assert.NotNil(t, p)
	})
}

func TestImageConformsTo(t *testing.T) {
	withImageParser(t, "./testdata/image/futuristic_tales.cbz", func(p *pub.Builder) {
		require.NotNil(t, p)
		pub := p.Build()
		require.NotNil(t, pub)

		assert.Equal(t, pub.Manifest.Metadata.ConformsTo, manifest.Profiles{manifest.ProfileDivina})
	})
}

func TestImageReadingOrderAlphabetical(t *testing.T) {
	withImageParser(t, "./testdata/image/futuristic_tales.cbz", func(p *pub.Builder) {
		require.NotNil(t, p)
		pub := p.Build()
		require.NotNil(t, pub)
		base, _ := url.URLFromDecodedPath("Cory Doctorow's Futuristic Tales of the Here and Now/")

		hrefs := make([]string, 0, len(pub.Manifest.ReadingOrder))
		for _, roi := range pub.Manifest.ReadingOrder {
			hrefs = append(hrefs, base.Relativize(roi.URL(nil, nil)).String())
		}
		assert.Exactly(t, []string{
			"a-fc.jpg", "x-002.jpg", "x-003.jpg", "x-004.jpg",
		}, hrefs, "readingOrder should be sorted alphabetically")
	})
}

func TestImageComicArchiveFolderReadingOrderAndTOC(t *testing.T) {
	a := staticAsset{mediaType: mediatype.Binary}
	f := staticLinksFetcher{
		links: manifest.LinkList{
			{Href: manifest.MustNewHREFFromString("Chapter 10.cbz", false), MediaType: &mediatype.CBZ},
			{Href: manifest.MustNewHREFFromString("ComicInfo.xml", false), MediaType: &mediatype.XML},
			{Href: manifest.MustNewHREFFromString("Chapter 2.cbr", false)},
			{Href: manifest.MustNewHREFFromString("Chapter 1.cbz", false)},
		},
	}

	builder, err := ImageParser{}.Parse(t.Context(), a, f)
	require.NoError(t, err)
	require.NotNil(t, builder)

	pub := builder.Build()
	require.Len(t, pub.Manifest.ReadingOrder, 3)
	assert.Equal(t, "Chapter%201.cbz", pub.Manifest.ReadingOrder[0].Href.String())
	assert.Equal(t, "Chapter%202.cbr", pub.Manifest.ReadingOrder[1].Href.String())
	assert.Equal(t, "Chapter%2010.cbz", pub.Manifest.ReadingOrder[2].Href.String())
	assert.Equal(t, &mediatype.CBZ, pub.Manifest.ReadingOrder[0].MediaType)
	assert.Equal(t, &mediatype.CBR, pub.Manifest.ReadingOrder[1].MediaType)
	assert.Empty(t, pub.Manifest.ReadingOrder[0].Rels)
	assert.True(t, pub.Manifest.ConformsTo(manifest.ProfileDivina))

	require.Len(t, pub.Manifest.TableOfContents, 3)
	assert.Equal(t, "Chapter 1", pub.Manifest.TableOfContents[0].Title)
	assert.Equal(t, "Chapter 2", pub.Manifest.TableOfContents[1].Title)
	assert.Equal(t, "Chapter 10", pub.Manifest.TableOfContents[2].Title)
	assert.Equal(t, pub.Manifest.ReadingOrder[0].Href.String(), pub.Manifest.TableOfContents[0].Href.String())
	assert.Equal(t, &mediatype.CBZ, pub.Manifest.TableOfContents[0].MediaType)
}

func TestImageComicArchiveFolderRejectsUnsupportedEntries(t *testing.T) {
	a := staticAsset{mediaType: mediatype.Binary}
	f := staticLinksFetcher{
		links: manifest.LinkList{
			{Href: manifest.MustNewHREFFromString("Chapter 1.cbz", false), MediaType: &mediatype.CBZ},
			{Href: manifest.MustNewHREFFromString("notes.pdf", false), MediaType: &mediatype.PDF},
		},
	}

	builder, err := ImageParser{}.Parse(t.Context(), a, f)
	require.NoError(t, err)
	assert.Nil(t, builder)
}

func TestImageCoverFirstItem(t *testing.T) {
	withImageParser(t, "./testdata/image/futuristic_tales.cbz", func(p *pub.Builder) {
		require.NotNil(t, p)
		pub := p.Build()
		require.NotNil(t, pub)

		coverItem := pub.Manifest.ReadingOrder.FirstWithRel("cover")
		require.NotNil(t, coverItem, "readingOrder should have an item with rel=cover")

		u, _ := url.URLFromDecodedPath("Cory Doctorow's Futuristic Tales of the Here and Now/a-fc.jpg")
		assert.Equal(t, manifest.NewHREF(u).String(), coverItem.Href.String())
	})
}

func TestImageTitleBasedOnRoot(t *testing.T) {
	withImageParser(t, "./testdata/image/futuristic_tales.cbz", func(p *pub.Builder) {
		require.NotNil(t, p)
		pub := p.Build()
		require.NotNil(t, pub)

		assert.Equal(
			t,
			"Cory Doctorow's Futuristic Tales of the Here and Now",
			pub.Manifest.Metadata.Title(),
			"publication title should be based on archive's root directory",
		)
	})
}
