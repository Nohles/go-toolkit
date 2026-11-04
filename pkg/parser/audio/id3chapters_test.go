package audio

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func safeSize(n int) []byte {
	return []byte{byte(n >> 21 & 0x7F), byte(n >> 14 & 0x7F), byte(n >> 7 & 0x7F), byte(n & 0x7F)}
}

func id3Frame(version byte, id string, body []byte) []byte {
	frame := []byte(id)
	if version == 4 {
		frame = append(frame, safeSize(len(body))...)
	} else {
		frame = binary.BigEndian.AppendUint32(frame, uint32(len(body)))
	}
	frame = append(frame, 0, 0)
	return append(frame, body...)
}

func chapFrame(version byte, id string, startMs uint32, title string) []byte {
	body := append([]byte(id), 0)
	body = binary.BigEndian.AppendUint32(body, startMs)
	body = binary.BigEndian.AppendUint32(body, startMs+1000)
	body = binary.BigEndian.AppendUint32(body, 0xFFFFFFFF)
	body = binary.BigEndian.AppendUint32(body, 0xFFFFFFFF)
	body = append(body, id3Frame(version, "TIT2", append([]byte{3}, title...))...)
	return id3Frame(version, "CHAP", body)
}

func txxxFrame(version byte, description, value string) []byte {
	body := append([]byte{3}, description...)
	body = append(body, 0)
	return id3Frame(version, "TXXX", append(body, value...))
}

func id3Tag(version byte, frames ...[]byte) []byte {
	var body []byte
	for _, frame := range frames {
		body = append(body, frame...)
	}
	tag := []byte{'I', 'D', '3', version, 0, 0}
	tag = append(tag, safeSize(len(body))...)
	return append(tag, body...)
}

func TestID3ChaptersReadsCHAPFramesInBothVersions(t *testing.T) {
	for _, version := range []byte{3, 4} {
		// A large cover ahead of the chapters must be skipped, not treated as frames.
		cover := id3Frame(version, "APIC", make([]byte, 200*1024))
		res, _ := bytesResource(id3Tag(version,
			cover,
			chapFrame(version, "ch1", 90_500, "Chapter 2"),
			chapFrame(version, "ch0", 0, "Opening Credits"),
		))
		chapters := id3Chapters(t.Context(), res)
		require.Len(t, chapters, 2, "ID3v2.%d", version)
		assert.Equal(t, chapterEntry{Title: "Opening Credits", Start: 0}, chapters[0])
		assert.Equal(t, chapterEntry{Title: "Chapter 2", Start: 90.5}, chapters[1])
	}
}

func TestID3ChaptersFallsBackToOverDriveMarkers(t *testing.T) {
	markers := "<Markers><Marker><Name>Title Page</Name><Time>0:00.000</Time></Marker>" +
		"<Marker><Name>Foreword</Name><Time>0:00.000</Time></Marker>" +
		"<Marker><Name>Chapter 1</Name><Time>12:30.500</Time></Marker>" +
		"<Marker><Name>Chapter 2</Name><Time>1:02:03.000</Time></Marker></Markers>"
	res, _ := bytesResource(id3Tag(3, txxxFrame(3, "OverDrive MediaMarkers", markers)))
	assert.Equal(t, []chapterEntry{
		{Title: "Foreword", Start: 0},
		{Title: "Chapter 1", Start: 750.5},
		{Title: "Chapter 2", Start: 3723},
	}, id3Chapters(t.Context(), res))
}

func TestID3ChaptersFallsBackToCueSheetTag(t *testing.T) {
	sheet := "TRACK 1 AUDIO\n  TITLE \"Chapter 01\"\n  INDEX 01 0:0:00\nTRACK 2 AUDIO\n  TITLE \"Chapter 02\"\n  INDEX 01 12:46:00\n"
	res, _ := bytesResource(id3Tag(3, txxxFrame(3, "cuesheet", sheet)))
	assert.Equal(t, []chapterEntry{{Title: "Chapter 01", Start: 0}, {Title: "Chapter 02", Start: 766}}, id3Chapters(t.Context(), res))
}

func TestID3ChaptersReadsUTF16Text(t *testing.T) {
	encode := func(value string) []byte {
		out := []byte{0xFF, 0xFE}
		for _, unit := range utf16.Encode([]rune(value)) {
			out = binary.LittleEndian.AppendUint16(out, unit)
		}
		return out
	}
	body := append([]byte{1}, encode("OverDrive MediaMarkers")...)
	body = append(body, 0, 0)
	body = append(body, encode("<Markers><Marker><Name>Kapitel 1</Name><Time>0:05.000</Time></Marker></Markers>")...)
	res, _ := bytesResource(id3Tag(3, id3Frame(3, "TXXX", body)))
	assert.Equal(t, []chapterEntry{{Title: "Kapitel 1", Start: 5}}, id3Chapters(t.Context(), res))
}

func TestID3ChaptersIgnoresDamagedTags(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		[]byte("not a tag at all"),
		append(id3Tag(3, chapFrame(3, "c", 0, "Whole"))[:12], 0xFF, 0xFF),
		id3Tag(2),
		append([]byte{'I', 'D', '3', 3, 0, 0, 0x7F, 0x7F, 0x7F, 0x7F}, []byte("CHAP")...),
	} {
		res, _ := bytesResource(data)
		assert.Empty(t, id3Chapters(t.Context(), res))
	}
}

func TestCombineFileChaptersKeepsFilesWithoutChapters(t *testing.T) {
	link := func(name, title string) manifest.Link {
		return manifest.Link{Href: manifest.MustNewHREFFromString(name, false), Title: title}
	}
	order := manifest.LinkList{link("a.mp3", "Part A"), link("b.mp3", "Part B")}
	assert.Nil(t, combineFileChapters(make([]manifest.LinkList, 2), order))

	combined := combineFileChapters([]manifest.LinkList{
		nil,
		chaptersToLinks([]chapterEntry{{Title: "One", Start: 0}, {Title: "Two", Start: 30}}, order[1]),
	}, order)
	require.Len(t, combined, 3)
	assert.Equal(t, "Part A", combined[0].Title)
	assert.Equal(t, "One", combined[1].Title)
	assert.Equal(t, "Two", combined[2].Title)
}

func TestMP3HeaderForJoinedFileIsIgnored(t *testing.T) {
	// The measured case: an Info header left over from the first of the joined
	// files, which describes 10.6 MB of a 585 MB file.
	assert.False(t, mp3HeaderDescribesFile(1324, 10_590_666, 584_819_527))
	// No byte count to compare, but no MP3 runs at 3.5 Mbps.
	assert.False(t, mp3HeaderDescribesFile(1324, 0, 584_819_527))
	// A real VBR header, with an ID3v1 tag after the audio it counted.
	assert.True(t, mp3HeaderDescribesFile(73_098, 584_000_000, 584_000_128))
	assert.True(t, mp3HeaderDescribesFile(73_098, 0, 584_000_000))
}

func TestCombineFileChaptersJoinsAChapterRunningAcrossFiles(t *testing.T) {
	first := manifest.Link{Href: manifest.MustNewHREFFromString("a.mp3", false), Title: "Part A"}
	second := manifest.Link{Href: manifest.MustNewHREFFromString("b.mp3", false), Title: "Part B"}
	combined := combineFileChapters([]manifest.LinkList{
		chaptersToLinks([]chapterEntry{{Title: "Chapter 1", Start: 0}}, first),
		chaptersToLinks([]chapterEntry{{Title: "Chapter 1", Start: 0}, {Title: "Chapter 2", Start: 40}}, second),
	}, manifest.LinkList{first, second})
	require.Len(t, combined, 2)
	assert.Equal(t, "Chapter 1", combined[0].Title)
	assert.Equal(t, "Chapter 2", combined[1].Title)
}
