package streamer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestListItems(t *testing.T) {
	list := NewManifestList()
	list.Set(ManifestListItem{
		Manifest:     "https://example.com/webpub/book-one/manifest.json",
		ManifestType: ManifestSourceDirectory,
		DirFile:      "book-one",
	})
	list.Set(ManifestListItem{
		Manifest:     "https://example.com/webpub/book-two.epub/manifest.json",
		ManifestType: ManifestSourceFile,
		DirFile:      "book-two.epub",
	})
	list.Set(ManifestListItem{
		Manifest:     "https://example.com/webpub/book-one/manifest.json",
		ManifestType: ManifestSourceFile,
		DirFile:      "book-one.epub",
	})

	assert.Equal(t, []ManifestListItem{
		{
			Manifest:     "https://example.com/webpub/book-one/manifest.json",
			ManifestType: ManifestSourceFile,
			DirFile:      "book-one.epub",
		},
		{
			Manifest:     "https://example.com/webpub/book-two.epub/manifest.json",
			ManifestType: ManifestSourceFile,
			DirFile:      "book-two.epub",
		},
	}, list.Items())

	list.Delete("https://example.com/webpub/book-one/manifest.json")
	assert.Equal(t, []ManifestListItem{
		{
			Manifest:     "https://example.com/webpub/book-two.epub/manifest.json",
			ManifestType: ManifestSourceFile,
			DirFile:      "book-two.epub",
		},
	}, list.Items())
}

func TestManifestListServesListJSON(t *testing.T) {
	list := NewManifestList(ManifestListItem{
		Manifest:     "https://example.com/webpub/book/manifest.json",
		ManifestType: ManifestSourceDirectory,
		DirFile:      "book",
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, ManifestListPath, nil)

	list.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var items []ManifestListItem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
	assert.Equal(t, list.Items(), items)
}

func TestManifestListOnlyServesListJSONPath(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/webpub/manifest.json", nil)

	NewManifestList().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestManifestListRejectsUnsupportedMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, ManifestListPath, nil)

	NewManifestList().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
}
