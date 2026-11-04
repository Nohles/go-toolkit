package audio

import (
	"context"
	"encoding/binary"
	"encoding/xml"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/nohles/go-toolkit/pkg/fetcher"
)

const (
	// Frames are read through a small window so a large cover picture is skipped,
	// not downloaded.
	id3ReadWindow = 64 * 1024
	// A chapter frame or marker list is text. Anything larger is not one.
	id3MaxTextFrame = 2 * 1024 * 1024
)

// id3Chapters extracts the chapters of an MP3 from its ID3v2 tag. CHAP frames are
// the standard, so they win over OverDrive's MediaMarkers, which in turn win over
// a cue sheet stored in a tag.
func id3Chapters(ctx context.Context, res fetcher.Resource) []chapterEntry {
	frames := readID3Frames(ctx, res)
	if len(frames.chapters) > 0 {
		return cleanChapters(frames.chapters)
	}
	if markers := parseOverDriveMarkers(frames.overDriveMarkers); len(markers) > 0 {
		return cleanChapters(markers)
	}
	return cleanChapters(chaptersFromCueSheet(frames.cueSheet))
}

type id3Frames struct {
	chapters         []chapterEntry
	overDriveMarkers string
	cueSheet         string
}

func readID3Frames(ctx context.Context, res fetcher.Resource) id3Frames {
	var found id3Frames
	header := readRange(ctx, res, 0, 10)
	if len(header) < 10 || string(header[:3]) != "ID3" {
		return found
	}
	// CHAP and TXXX as read here exist from ID3v2.3 on.
	version := header[3]
	if version != 3 && version != 4 {
		return found
	}
	end := int64(10) + int64(synchsafe(header[6:10]))

	offset := int64(10)
	if header[5]&0x40 != 0 { // Extended header.
		extended := readRange(ctx, res, offset, 4)
		if len(extended) < 4 {
			return found
		}
		if version == 4 {
			offset += int64(synchsafe(extended)) // Counts itself.
		} else {
			offset += int64(binary.BigEndian.Uint32(extended)) + 4
		}
	}

	var window []byte
	var windowStart int64
	read := func(at, length int64) []byte {
		if at >= windowStart && at+length <= windowStart+int64(len(window)) {
			return window[at-windowStart : at-windowStart+length]
		}
		size := max(length, id3ReadWindow)
		size = min(size, end-at)
		if size < length {
			return nil
		}
		window = readRange(ctx, res, at, size)
		windowStart = at
		if int64(len(window)) < length {
			return nil
		}
		return window[:length]
	}

	for offset+10 <= end {
		frame := read(offset, 10)
		if frame == nil || frame[0] == 0 {
			break
		}
		id := string(frame[:4])
		size := int64(id3FrameSize(frame[4:8], version))
		offset += 10
		if size <= 0 || offset+size > end {
			break
		}
		if (id == "CHAP" || id == "TXXX") && size <= id3MaxTextFrame {
			if body := read(offset, size); body != nil {
				switch id {
				case "CHAP":
					if chapter, ok := parseCHAPFrame(body, version); ok {
						found.chapters = append(found.chapters, chapter)
					}
				case "TXXX":
					description, value := parseTXXXFrame(body)
					switch strings.ToLower(description) {
					case "overdrive mediamarkers":
						found.overDriveMarkers = value
					case "cuesheet":
						found.cueSheet = value
					}
				}
			}
		}
		offset += size
	}
	return found
}

func synchsafe(b []byte) uint32 {
	return uint32(b[0]&0x7F)<<21 | uint32(b[1]&0x7F)<<14 | uint32(b[2]&0x7F)<<7 | uint32(b[3]&0x7F)
}

func id3FrameSize(b []byte, version byte) uint32 {
	if version == 4 {
		return synchsafe(b)
	}
	return binary.BigEndian.Uint32(b)
}

// parseCHAPFrame reads a CHAP frame: an element id, four 32-bit times and offsets,
// then sub-frames, one of which (TIT2) names the chapter.
func parseCHAPFrame(body []byte, version byte) (chapterEntry, bool) {
	idEnd := indexByte(body, 0)
	if idEnd < 0 || idEnd+17 > len(body) {
		return chapterEntry{}, false
	}
	start := float64(binary.BigEndian.Uint32(body[idEnd+1:idEnd+5])) / 1000
	entry := chapterEntry{Start: start}
	for sub := body[idEnd+17:]; len(sub) >= 10 && sub[0] != 0; {
		size := int(id3FrameSize(sub[4:8], version))
		if size < 0 || 10+size > len(sub) {
			break
		}
		if string(sub[:4]) == "TIT2" && size > 0 {
			entry.Title = strings.TrimSpace(decodeID3Text(sub[10:10+size], true))
			break
		}
		sub = sub[10+size:]
	}
	return entry, true
}

// parseTXXXFrame reads a user-defined text frame: encoding, description, value.
func parseTXXXFrame(body []byte) (string, string) {
	if len(body) < 2 {
		return "", ""
	}
	encoding := body[0]
	text := body[1:]
	cut := -1
	if encoding == 1 || encoding == 2 { // UTF-16: the terminator is two aligned zero bytes.
		for i := 0; i+1 < len(text); i += 2 {
			if text[i] == 0 && text[i+1] == 0 {
				cut = i
				break
			}
		}
		if cut < 0 {
			return "", ""
		}
		value := text[cut+2:]
		return decodeID3Bytes(encoding, text[:cut], nil), decodeID3Bytes(encoding, value, text[:cut])
	}
	cut = indexByte(text, 0)
	if cut < 0 {
		return "", ""
	}
	return decodeID3Bytes(encoding, text[:cut], nil), decodeID3Bytes(encoding, text[cut+1:], nil)
}

// decodeID3Text decodes a text frame's body: one encoding byte, then text.
func decodeID3Text(body []byte, firstValue bool) string {
	if len(body) == 0 {
		return ""
	}
	text := decodeID3Bytes(body[0], body[1:], nil)
	if firstValue {
		if i := strings.IndexByte(text, 0); i >= 0 {
			text = text[:i]
		}
	}
	return text
}

// decodeID3Bytes decodes text in an ID3 encoding. A UTF-16 value without its own
// byte-order mark takes the one that opened the description before it.
func decodeID3Bytes(encoding byte, b []byte, bomFrom []byte) string {
	switch encoding {
	case 0:
		runes := make([]rune, len(b))
		for i, c := range b {
			runes[i] = rune(c)
		}
		return strings.TrimRight(string(runes), "\x00")
	case 1, 2:
		order := binary.ByteOrder(binary.BigEndian)
		bomSource := b
		if encoding == 1 && (len(bomSource) < 2 || !hasBOM(bomSource)) && len(bomFrom) >= 2 {
			bomSource = bomFrom
		}
		if encoding == 1 && len(bomSource) >= 2 && bomSource[0] == 0xFF && bomSource[1] == 0xFE {
			order = binary.LittleEndian
		}
		if hasBOM(b) {
			b = b[2:]
		}
		units := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			units = append(units, order.Uint16(b[i:i+2]))
		}
		return strings.TrimRight(string(utf16.Decode(units)), "\x00")
	default:
		return strings.TrimRight(string(b), "\x00")
	}
}

func hasBOM(b []byte) bool {
	return len(b) >= 2 && ((b[0] == 0xFF && b[1] == 0xFE) || (b[0] == 0xFE && b[1] == 0xFF))
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// parseOverDriveMarkers reads OverDrive's "<Markers><Marker><Name/><Time/>" list.
func parseOverDriveMarkers(value string) []chapterEntry {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var parsed struct {
		Markers []struct {
			Name string `xml:"Name"`
			Time string `xml:"Time"`
		} `xml:"Marker"`
	}
	if err := xml.Unmarshal([]byte(value), &parsed); err != nil {
		return nil
	}
	entries := make([]chapterEntry, 0, len(parsed.Markers))
	for _, marker := range parsed.Markers {
		start, ok := parseMarkerTime(marker.Time)
		if !ok {
			continue
		}
		entries = append(entries, chapterEntry{Title: strings.TrimSpace(marker.Name), Start: start})
	}
	return entries
}

// parseMarkerTime parses "SS.mmm", "M:SS.mmm" or "H:MM:SS.mmm" into seconds.
func parseMarkerTime(value string) (float64, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, part := range parts {
		number, err := strconv.ParseFloat(part, 64)
		if err != nil || number < 0 {
			return 0, false
		}
		total = total*60 + number
	}
	return total, true
}

// chaptersFromCueSheet reads a cue sheet stored in a tag, which names no FILE
// because it describes the file holding it.
func chaptersFromCueSheet(value string) []chapterEntry {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	sheet := value
	if !strings.Contains(strings.ToUpper(sheet), "FILE ") {
		sheet = "FILE \"self\" MP3\n" + sheet
	}
	parsed := parseCUE([]byte(sheet))
	entries := make([]chapterEntry, 0, len(parsed))
	for _, entry := range parsed {
		entries = append(entries, chapterEntry{Title: strings.TrimSpace(entry.Title), Start: entry.Start})
	}
	return entries
}

// cleanChapters orders chapters by start. Markers sharing a start hold nothing
// before the next one, so the last of them names the place.
func cleanChapters(entries []chapterEntry) []chapterEntry {
	if len(entries) == 0 {
		return nil
	}
	sorted := append([]chapterEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	cleaned := sorted[:0]
	for _, entry := range sorted {
		if n := len(cleaned); n > 0 && cleaned[n-1].Start == entry.Start {
			cleaned[n-1] = entry
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}
