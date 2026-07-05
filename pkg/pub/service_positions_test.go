package pub

import (
	"encoding/json"
	"testing"

	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/internal/extensions"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/util/url"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerResourcePositionsServiceEmptyReadingOrder(t *testing.T) {
	service := PerResourcePositionsService{}
	assert.Equal(t, 0, len(service.Positions(t.Context())))
}

func TestPerResourcePositionsServiceSingleReadingOrder(t *testing.T) {
	service := PerResourcePositionsService{
		readingOrder: manifest.LinkList{{Href: manifest.MustNewHREFFromString("res", false), MediaType: &mediatype.PNG}},
	}

	assert.Equal(t, []manifest.Locator{{
		Href:      url.MustURLFromString("res"),
		MediaType: mediatype.PNG,
		Locations: manifest.Locations{
			Position:         extensions.Pointer(uint(1)),
			TotalProgression: extensions.Pointer(float64(0.0)),
		},
	}}, service.Positions(t.Context()))
}

func TestPerResourcePositionsServiceMultiReadingOrder(t *testing.T) {
	service := PerResourcePositionsService{
		readingOrder: manifest.LinkList{
			{Href: manifest.MustNewHREFFromString("res", false)},
			{Href: manifest.MustNewHREFFromString("chap1", false), MediaType: &mediatype.PNG},
			{Href: manifest.MustNewHREFFromString("chap2", false), MediaType: &mediatype.PNG, Title: "Chapter 2"},
		},
		fallbackMediaType: mediatype.Binary,
	}

	assert.Equal(t, []manifest.Locator{
		{
			Href:      url.MustURLFromString("res"),
			MediaType: mediatype.Binary,
			Locations: manifest.Locations{
				Position:         extensions.Pointer(uint(1)),
				TotalProgression: extensions.Pointer(float64(0.0)),
			},
		},
		{
			Href:      url.MustURLFromString("chap1"),
			MediaType: mediatype.PNG,
			Locations: manifest.Locations{
				Position:         extensions.Pointer(uint(2)),
				TotalProgression: extensions.Pointer(float64(1.0 / 3.0)),
			},
		},
		{
			Href:      url.MustURLFromString("chap2"),
			MediaType: mediatype.PNG,
			Title:     "Chapter 2",
			Locations: manifest.Locations{
				Position:         extensions.Pointer(uint(3)),
				TotalProgression: extensions.Pointer(float64(2.0 / 3.0)),
			},
		},
	}, service.Positions(t.Context()))
}

func TestPerResourcePositionsServiceMediaTypeFallback(t *testing.T) {
	service := PerResourcePositionsService{
		readingOrder:      manifest.LinkList{{Href: manifest.MustNewHREFFromString("res", false)}},
		fallbackMediaType: mediatype.MustNewOfString("image/*"),
	}

	mt, _ := mediatype.NewOfString("image/*")
	assert.Equal(t, []manifest.Locator{{
		Href:      url.MustURLFromString("res"),
		MediaType: mt,
		Locations: manifest.Locations{
			Position:         extensions.Pointer(uint(1)),
			TotalProgression: extensions.Pointer(float64(0.0)),
		},
	}}, service.Positions(t.Context()))
}

func TestPositionsServiceDocumentIncludesCurrentChapter(t *testing.T) {
	service := PerResourcePositionsService{
		readingOrder: manifest.LinkList{
			{Href: manifest.MustNewHREFFromString("Chapter 1.cbz", false), MediaType: &mediatype.CBZ, Title: "Chapter 1"},
			{Href: manifest.MustNewHREFFromString("Chapter 2.cbz", false), MediaType: &mediatype.CBZ, Title: "Chapter 2"},
		},
	}
	resource, ok := GetForPositionsService(t.Context(), service, PositionsLink)
	require.True(t, ok)

	data, err := fetcher.ReadResourceAsJSON(t.Context(), resource)
	require.Nil(t, err)
	require.NotNil(t, data)

	raw, jerr := json.Marshal(data["currentChapter"])
	require.NoError(t, jerr)
	var locator manifest.Locator
	jerr = json.Unmarshal(raw, &locator)
	require.NoError(t, jerr)

	require.NotNil(t, locator.Locations.Position)
	assert.Equal(t, uint(1), *locator.Locations.Position)
	assert.Equal(t, "Chapter%201.cbz", locator.Href.String())
	assert.Equal(t, "Chapter 1", locator.Title)
}
