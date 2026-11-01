package inspector

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func id3Audio(t *testing.T, frames [][2]string) string {
	t.Helper()
	var body bytes.Buffer
	for _, frame := range frames {
		value := append([]byte{0}, []byte(frame[1])...)
		body.WriteString(frame[0])
		require.NoError(t, binary.Write(&body, binary.BigEndian, uint32(len(value))))
		body.Write([]byte{0, 0})
		body.Write(value)
	}
	size := body.Len()
	header := []byte{'I', 'D', '3', 3, 0, 0, byte(size>>21) & 0x7f, byte(size>>14) & 0x7f, byte(size>>7) & 0x7f, byte(size) & 0x7f}
	file := filepath.Join(t.TempDir(), "audio.mp3")
	require.NoError(t, os.WriteFile(file, append(header, body.Bytes()...), 0600))
	return file
}

func TestInspectAudioReadsExplicitID3UserText(t *testing.T) {
	file := id3Audio(t, [][2]string{{"TIT2", "Locked Rooms"}, {"TPE1", "Jenny Sterlin"}, {"TXXX", "author\x00Laurie R. King\x00"}, {"TXXX", "narrator\x00Jenny Sterlin\x00"}, {"TXXX", "isbn\x009780804139038\x00"}})
	result, err := Inspect(Request{FilePath: file, MediaKind: "audiobooks", IncludeMetadata: true})
	require.NoError(t, err)
	require.Equal(t, []string{"Laurie R. King"}, result.Metadata["authors"])
	require.Equal(t, []string{"Jenny Sterlin"}, result.Metadata["narrators"])
	require.Equal(t, "9780804139038", result.Metadata["isbn"])
}

func TestInspectAudioDoesNotInferNarratorFromDifferentAlbumArtist(t *testing.T) {
	file := id3Audio(t, [][2]string{{"TIT2", "Book"}, {"TPE1", "Jenny Sterlin"}, {"TPE2", "Laurie R. King"}, {"TXXX", "author\x00Laurie R. King"}})
	result, err := Inspect(Request{FilePath: file, MediaKind: "audiobooks", IncludeMetadata: true})
	require.NoError(t, err)
	require.Equal(t, []string{"Laurie R. King"}, result.Metadata["authors"])
	require.NotContains(t, result.Metadata, "narrators")
}

func TestInspectEPUBDoesNotTreatOtherIdentifiersAsISBN(t *testing.T) {
	out := map[string]any{}
	var p opfPackage
	p.Metadata.Identifiers = []string{"urn:uuid:9780804139038"}
	applyEPUBMetadata(out, p)
	require.NotContains(t, out, "isbn")
}

func TestInspectAudioDoesNotUseKnownNarratorAsAuthor(t *testing.T) {
	file := id3Audio(t, [][2]string{{"TIT2", "Locked Rooms"}, {"TPE1", "Jenny Sterlin"}, {"TXXX", "narrator\x00Jenny Sterlin"}})
	result, err := Inspect(Request{FilePath: file, MediaKind: "audiobooks", IncludeMetadata: true})
	require.NoError(t, err)
	require.NotContains(t, result.Metadata, "authors")
	require.Equal(t, []string{"Jenny Sterlin"}, result.Metadata["narrators"])
}

func TestNormalizedISBNValidatesAndPreservesOnlyIsbnValues(t *testing.T) {
	for _, v := range []string{"9780804139038", "urn:isbn:978-0-8041-3903-8", "178110008X"} {
		require.NotEmpty(t, normalizedISBN(v), v)
	}
	for _, v := range []string{"urn:uuid:9780804139038", "9780804139039", "1234567890", "other9780804139038"} {
		require.Empty(t, normalizedISBN(v), v)
	}
}

func TestInspectAudioPreservesSurnameFirstAuthor(t *testing.T) {
	file := id3Audio(t, [][2]string{{"TIT2", "The Tiger in the Well"}, {"TPE1", "Pullman, Philip"}})
	result, err := Inspect(Request{FilePath: file, MediaKind: "audiobooks", IncludeMetadata: true})
	require.NoError(t, err)
	require.Equal(t, []string{"Pullman, Philip"}, result.Metadata["authors"])
}
