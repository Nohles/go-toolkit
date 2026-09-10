package inspector

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var tinyPNG = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

func writeZIP(t *testing.T, name string, files map[string][]byte) string {
	t.Helper()
	filePath := filepath.Join(t.TempDir(), name)
	f, err := os.Create(filePath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for fileName, data := range files {
		entry, createErr := w.Create(fileName)
		require.NoError(t, createErr)
		_, writeErr := entry.Write(data)
		require.NoError(t, writeErr)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
	return filePath
}

func TestInspectEPUBMetadataAndDeclaredCover(t *testing.T) {
	filePath := writeZIP(t, "book.epub", map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container><rootfiles><rootfile full-path="EPUB/package.opf"/></rootfiles></container>`),
		"EPUB/package.opf":       []byte(`<?xml version="1.0"?><package><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Book Title</dc:title><dc:creator>Author</dc:creator><dc:identifier>9781234567897</dc:identifier><meta name="cover" content="cover-id"/></metadata><manifest><item id="cover-id" href="images/front.png" media-type="image/png"/></manifest></package>`),
		"EPUB/images/front.png":  tinyPNG,
	})
	result, err := Inspect(Request{FilePath: filePath, MediaKind: "books", IncludeMetadata: true, IncludeCover: true})
	require.NoError(t, err)
	require.Equal(t, "Book Title", result.Metadata["title"])
	require.Equal(t, []string{"Author"}, result.Metadata["authors"])
	require.NotNil(t, result.Cover)
	require.Equal(t, "EPUB/images/front.png", result.Cover.Locator)
	require.Equal(t, tinyPNG, result.Cover.Bytes)
}

func TestInspectComicHonorsFrontCoverAndZipDisguisedAsCBR(t *testing.T) {
	filePath := writeZIP(t, "issue.cbr", map[string][]byte{
		"ComicInfo.xml": []byte(`<ComicInfo><Title>Issue Title</Title><Series>Series</Series><Pages><Page Image="1" Type="FrontCover"/></Pages></ComicInfo>`),
		"001.png":       tinyPNG,
		"002.jpg":       {0xff, 0xd8, 0xff, 0xd9},
	})
	result, err := Inspect(Request{FilePath: filePath, MediaKind: "comics", IncludeMetadata: true, IncludeCover: true})
	require.NoError(t, err)
	require.Equal(t, "Issue Title", result.Metadata["title"])
	require.NotNil(t, result.Cover)
	require.Equal(t, "002.jpg", result.Cover.Locator)
}

func TestInspectRealCBRIsUnsupported(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "issue.cbr")
	require.NoError(t, os.WriteFile(filePath, []byte("Rar!not-a-zip"), 0o600))
	_, err := Inspect(Request{FilePath: filePath, MediaKind: "comics", IncludeCover: true})
	require.ErrorIs(t, err, ErrUnsupported)
}
