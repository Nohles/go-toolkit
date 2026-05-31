package parser

import (
	"testing"
	"time"

	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/simonhull/audiometa"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAudioMetadataEnrichesManifestFirstCompleteWins(t *testing.T) {
	published := time.Date(2024, time.May, 1, 0, 0, 0, 0, time.UTC)
	enrichment := audioPublicationEnrichment{
		tracks: []audioTrackMetadata{
			{
				title:    "Track One",
				duration: 10,
				bitrate:  128,
				size:     1000,
			},
			{
				album:       "The Book",
				subtitle:    "A Subtitle",
				description: "A description",
				language:    "en",
				publisher:   "Publisher",
				authors:     []string{"Author"},
				narrators:   []string{"Narrator"},
				genres:      []string{"Fiction", "Adventure"},
				published:   &published,
				duration:    20,
			},
		},
	}

	md := manifest.Metadata{
		LocalizedTitle: manifest.NewLocalizedStringFromString("Fallback"),
		ConformsTo:     manifest.Profiles{manifest.ProfileAudiobook},
	}
	enrichment.enrichMetadata(&md, "Fallback", 2)

	assert.Equal(t, "The Book", md.Title())
	assert.Equal(t, "A Subtitle", md.Subtitle())
	assert.Equal(t, "A description", md.Description)
	assert.Equal(t, manifest.Strings{"en"}, md.Languages)
	require.Len(t, md.Publishers, 1)
	assert.Equal(t, "Publisher", md.Publishers[0].Name())
	require.Len(t, md.Authors, 1)
	assert.Equal(t, "Author", md.Authors[0].Name())
	require.Len(t, md.Narrators, 1)
	assert.Equal(t, "Narrator", md.Narrators[0].Name())
	assert.Equal(t, []manifest.Subject{
		{LocalizedName: manifest.NewLocalizedStringFromString("Fiction")},
		{LocalizedName: manifest.NewLocalizedStringFromString("Adventure")},
	}, md.Subjects)
	require.NotNil(t, md.Published)
	assert.Equal(t, published, *md.Published)
	require.NotNil(t, md.Duration)
	assert.Equal(t, 30.0, *md.Duration)
}

func TestAudioMetadataTitleFallbacks(t *testing.T) {
	single := audioPublicationEnrichment{
		tracks: []audioTrackMetadata{{title: "Single Track"}},
	}
	md := manifest.Metadata{LocalizedTitle: manifest.NewLocalizedStringFromString("Fallback")}
	single.enrichMetadata(&md, "Fallback", 1)
	assert.Equal(t, "Single Track", md.Title())

	multi := audioPublicationEnrichment{
		tracks: []audioTrackMetadata{{title: "Track One"}, {title: "Track Two"}},
	}
	md = manifest.Metadata{LocalizedTitle: manifest.NewLocalizedStringFromString("Fallback")}
	multi.enrichMetadata(&md, "Fallback", 2)
	assert.Equal(t, "Fallback", md.Title())
}

func TestAudioMetadataEnrichesReadingOrder(t *testing.T) {
	enrichment := audioPublicationEnrichment{
		tracks: []audioTrackMetadata{{
			title:    "Chapter One",
			duration: 123.5,
			bitrate:  64,
			size:     42,
		}},
	}

	readingOrder := enrichment.enrichReadingOrder(manifest.LinkList{{
		Href:      manifest.MustNewHREFFromString("one.mp3", false),
		MediaType: &mediatype.MP3,
	}})

	require.Len(t, readingOrder, 1)
	assert.Equal(t, "Chapter One", readingOrder[0].Title)
	assert.Equal(t, 123.5, readingOrder[0].Duration)
	assert.Equal(t, 64.0, readingOrder[0].Bitrate)
	assert.Equal(t, uint(42), readingOrder[0].Size)
}

func TestAudioMetadataEnrichesSizeWhenOnlySizeIsKnown(t *testing.T) {
	enrichment := audioPublicationEnrichment{
		tracks: []audioTrackMetadata{{size: 42}},
	}

	readingOrder := enrichment.enrichReadingOrder(manifest.LinkList{{
		Href:      manifest.MustNewHREFFromString("one.wav", false),
		MediaType: &mediatype.WAV,
	}})

	require.Len(t, readingOrder, 1)
	assert.Equal(t, uint(42), readingOrder[0].Size)
	assert.Empty(t, readingOrder[0].Title)
	assert.Zero(t, readingOrder[0].Duration)
	assert.Zero(t, readingOrder[0].Bitrate)
}

func TestAudioMetadataTOCUsesChaptersAndTrackFallbacks(t *testing.T) {
	enrichment := audioPublicationEnrichment{
		tracks: []audioTrackMetadata{
			{
				chapters: []audioChapterMetadata{
					{title: "Section One", start: 5500 * time.Millisecond},
				},
			},
			{
				title: "Second Track",
			},
		},
	}
	readingOrder := manifest.LinkList{
		{Href: manifest.MustNewHREFFromString("one.mp3", false)},
		{Href: manifest.MustNewHREFFromString("two.mp3", false), Title: "Second Track"},
	}

	toc := enrichment.tableOfContents(readingOrder)

	require.Len(t, toc, 2)
	assert.Equal(t, "one.mp3#t=5.5", toc[0].Href.String())
	assert.Equal(t, "Section One", toc[0].Title)
	assert.Equal(t, "two.mp3#t=0", toc[1].Href.String())
	assert.Equal(t, "Second Track", toc[1].Title)
}

func TestAudioCoverServiceServesCoverBytes(t *testing.T) {
	enrichment := audioPublicationEnrichment{
		cover: &audioCoverMetadata{
			mediaType: mediatype.PNG,
			data:      []byte{1, 2, 3},
			width:     10,
			height:    20,
		},
	}

	link := enrichment.coverLink()
	require.NotNil(t, link)
	assert.Equal(t, audioCoverHref, link.Href.String())
	assert.Equal(t, manifest.Strings{"cover"}, link.Rels)
	assert.Equal(t, &mediatype.PNG, link.MediaType)
	assert.Equal(t, uint(3), link.Size)
	assert.Equal(t, uint(10), link.Width)
	assert.Equal(t, uint(20), link.Height)

	service := enrichment.coverServiceFactory()(pub.Context{}, false)
	resource, ok := service.Get(t.Context(), *link)
	require.True(t, ok)
	data, err := resource.Read(t.Context(), 0, 0)
	require.Nil(t, err)
	assert.Equal(t, []byte{1, 2, 3}, data)
}

func TestAudioCoverSelectionPrefersFrontCover(t *testing.T) {
	cover := selectAudioCover(t.Context(), []audiometa.Artwork{
		{
			MIMEType: "image/png",
			Data:     []byte{1},
			Type:     audiometa.ArtworkBackCover,
		},
		{
			MIMEType: "image/jpeg",
			Data:     []byte{2},
			Type:     audiometa.ArtworkFrontCover,
			Width:    30,
			Height:   40,
		},
	})

	require.NotNil(t, cover)
	assert.Equal(t, mediatype.JPEG, cover.mediaType)
	assert.Equal(t, []byte{2}, cover.data)
	assert.Equal(t, uint(30), cover.width)
	assert.Equal(t, uint(40), cover.height)
}

func TestAudioCoverLinkAbsentWithoutArtwork(t *testing.T) {
	enrichment := audioPublicationEnrichment{}
	assert.Nil(t, enrichment.coverLink())
}

func TestAudioPublishedDateParsing(t *testing.T) {
	assert.Equal(t, time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC), *parseAudioPublishedDate("2024"))
	assert.Equal(t, time.Date(2024, time.May, 1, 0, 0, 0, 0, time.UTC), *parseAudioPublishedDate("2024-05-01"))
	assert.Equal(t, time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC), *parseAudioPublishedDate(2023))
	assert.Nil(t, parseAudioPublishedDate("not a date"))
}
