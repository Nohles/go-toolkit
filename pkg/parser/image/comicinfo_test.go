package image

import (
	"context"
	"testing"

	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// comicInfoFetcher serves fixed bytes for the files in `files` and lists the
// given links.
type comicInfoFetcher struct {
	staticLinksFetcher
	files map[string]string
}

func (f comicInfoFetcher) Get(ctx context.Context, link manifest.Link) fetcher.Resource {
	body, ok := f.files[link.Href.String()]
	if !ok {
		return f.staticLinksFetcher.Get(ctx, link)
	}
	return fetcher.NewBytesResource(link, func() []byte { return []byte(body) })
}

func pageLinks(names ...string) manifest.LinkList {
	links := make(manifest.LinkList, 0, len(names))
	for _, name := range names {
		links = append(links, manifest.Link{Href: manifest.MustNewHREFFromString(name, false), MediaType: &mediatype.JPEG})
	}
	return links
}

func parseWithComicInfo(t *testing.T, mediaType mediatype.MediaType, links manifest.LinkList, xml string) *manifest.Manifest {
	t.Helper()
	links = append(links, manifest.Link{Href: manifest.MustNewHREFFromString("ComicInfo.xml", false), MediaType: &mediatype.XML})
	f := comicInfoFetcher{
		staticLinksFetcher: staticLinksFetcher{links: links},
		files:              map[string]string{"ComicInfo.xml": xml},
	}
	builder, err := ImageParser{}.Parse(t.Context(), staticAsset{mediaType: mediaType}, f)
	require.NoError(t, err)
	require.NotNil(t, builder)
	pub := builder.Build()
	return &pub.Manifest
}

func TestComicInfoRightToLeftMangaSetsReadingProgression(t *testing.T) {
	m := parseWithComicInfo(t, mediatype.CBZ, pageLinks("001.jpg", "002.jpg"),
		`<ComicInfo><Manga>YesAndRightToLeft</Manga></ComicInfo>`)
	assert.Equal(t, manifest.RTL, m.Metadata.ReadingProgression)
}

func TestComicInfoPlainMangaLeavesReadingProgressionUnset(t *testing.T) {
	for _, value := range []string{"Yes", "No", "Unknown", ""} {
		m := parseWithComicInfo(t, mediatype.CBZ, pageLinks("001.jpg"),
			`<ComicInfo><Manga>`+value+`</Manga></ComicInfo>`)
		assert.Equal(t, manifest.None, m.Metadata.ReadingProgression, "Manga=%q", value)
	}
}

func TestComicInfoDoublePageMarksPageCenter(t *testing.T) {
	m := parseWithComicInfo(t, mediatype.CBZ, pageLinks("003.jpg", "001.jpg", "002.jpg", "004.jpg"),
		`<ComicInfo><Pages>
			<Page Image="0" Type="FrontCover"/>
			<Page Image="2" DoublePage="True"/>
			<Page Image="3" DoublePage="false"/>
			<Page Image="9" DoublePage="True"/>
		</Pages></ComicInfo>`)
	require.Len(t, m.ReadingOrder, 4)
	// Index 2 of the sorted reading order is 003.jpg.
	assert.Equal(t, "003.jpg", m.ReadingOrder[2].Href.String())
	assert.Equal(t, manifest.PageCenter, m.ReadingOrder[2].Properties.Page())
	for _, i := range []int{0, 1, 3} {
		assert.Equal(t, manifest.PageNone, m.ReadingOrder[i].Properties.Page(), "page %d", i)
	}
}

func TestComicInfoMalformedIsIgnored(t *testing.T) {
	m := parseWithComicInfo(t, mediatype.CBZ, pageLinks("001.jpg"), `<ComicInfo><Manga>YesAndRightToLeft`)
	assert.Equal(t, manifest.None, m.Metadata.ReadingProgression)
	require.Len(t, m.ReadingOrder, 1)
}

func TestComicInfoInFolderOfArchivesSetsDirectionOnly(t *testing.T) {
	links := manifest.LinkList{
		{Href: manifest.MustNewHREFFromString("Chapter 1.cbz", false), MediaType: &mediatype.CBZ},
		{Href: manifest.MustNewHREFFromString("Chapter 2.cbz", false), MediaType: &mediatype.CBZ},
	}
	m := parseWithComicInfo(t, mediatype.Binary, links,
		`<ComicInfo><Manga>YesAndRightToLeft</Manga><Pages><Page Image="0" DoublePage="True"/></Pages></ComicInfo>`)
	assert.Equal(t, manifest.RTL, m.Metadata.ReadingProgression)
	assert.Equal(t, manifest.PageNone, m.ReadingOrder[0].Properties.Page())
}

func TestFindComicInfoPrefersShallowest(t *testing.T) {
	link, ok := findComicInfoLink(manifest.LinkList{
		{Href: manifest.MustNewHREFFromString("nested/ComicInfo.xml", false)},
		{Href: manifest.MustNewHREFFromString("comicinfo.XML", false)},
	})
	require.True(t, ok)
	assert.Equal(t, "comicinfo.XML", link.Href.String())
}
