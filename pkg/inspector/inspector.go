// Package inspector provides bounded, scan-oriented publication inspection.
// It deliberately avoids constructing a full Readium publication.
package inspector

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dhowden/tag"
	readiumaudio "github.com/nohles/go-toolkit/pkg/parser/audio"
)

const ExtractorVersion = "reader-inspector-v1"

const (
	maxArchiveEntries   = 100_000
	maxMetadataBytes    = 4 << 20
	maxCoverBytes       = 64 << 20
	maxInspectedBytes   = 72 << 20
	maxCompressionRatio = 1_000
)

var ErrUnsupported = errors.New("publication format is unsupported")

type Request struct {
	FilePath          string `json:"filePath"`
	MediaKind         string `json:"mediaKind"`
	IncludeMetadata   bool   `json:"includeMetadata"`
	IncludeCover      bool   `json:"includeCover"`
	SourceFingerprint string `json:"sourceFingerprint,omitempty"`
}

type Cover struct {
	Bytes    []byte `json:"-"`
	MIMEType string `json:"mimeType"`
	Locator  string `json:"locator"`
}

type Diagnostics struct {
	Format         string `json:"format"`
	ElapsedMillis  int64  `json:"elapsedMillis"`
	InspectedBytes int64  `json:"inspectedBytes"`
}

type Result struct {
	Metadata          map[string]any `json:"metadata"`
	Cover             *Cover         `json:"cover,omitempty"`
	SourceFingerprint string         `json:"sourceFingerprint"`
	ExtractorVersion  string         `json:"extractorVersion"`
	Diagnostics       Diagnostics    `json:"diagnostics"`
}

type budget struct{ used int64 }

func (b *budget) read(entry *zip.File, limit int64) ([]byte, error) {
	if entry.FileInfo().IsDir() || entry.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("archive entry %q exceeds inspection limit", entry.Name)
	}
	if entry.CompressedSize64 == 0 && entry.UncompressedSize64 > 0 {
		return nil, fmt.Errorf("archive entry %q has invalid compression size", entry.Name)
	}
	if entry.CompressedSize64 > 0 && entry.UncompressedSize64/entry.CompressedSize64 > maxCompressionRatio {
		return nil, fmt.Errorf("archive entry %q exceeds compression ratio limit", entry.Name)
	}
	if b.used+int64(entry.UncompressedSize64) > maxInspectedBytes {
		return nil, errors.New("archive exceeds inspected byte limit")
	}
	r, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("archive entry %q exceeds inspection limit", entry.Name)
	}
	b.used += int64(len(data))
	return data, nil
}

func Inspect(req Request) (Result, error) {
	started := time.Now()
	result := Result{Metadata: map[string]any{}, ExtractorVersion: ExtractorVersion}
	info, err := os.Stat(req.FilePath)
	if err != nil {
		return result, err
	}
	result.SourceFingerprint = req.SourceFingerprint
	if result.SourceFingerprint == "" {
		result.SourceFingerprint = fmt.Sprintf("size:%d;mtime:%d", info.Size(), info.ModTime().UnixMilli())
	}
	ext := strings.ToLower(filepath.Ext(req.FilePath))
	switch req.MediaKind {
	case "books":
		result.Diagnostics.Format = "epub"
		err = inspectEPUB(req, &result)
	case "comics":
		result.Diagnostics.Format = "cbz"
		err = inspectComic(req, &result)
		if err != nil && ext == ".cbr" && errors.Is(err, zip.ErrFormat) {
			err = ErrUnsupported
		}
	case "audiobooks":
		result.Diagnostics.Format = "audio"
		err = inspectAudio(req, &result)
	default:
		err = ErrUnsupported
	}
	result.Diagnostics.ElapsedMillis = time.Since(started).Milliseconds()
	return result, err
}

func openArchive(filePath string) (*zip.ReadCloser, error) {
	z, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	if len(z.File) > maxArchiveEntries {
		z.Close()
		return nil, errors.New("archive exceeds entry count limit")
	}
	for _, f := range z.File {
		if !filepath.IsLocal(f.Name) || strings.Contains(f.Name, "\\") {
			z.Close()
			return nil, fmt.Errorf("unsafe archive path %q", f.Name)
		}
	}
	return z, nil
}

type epubContainer struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}
type opfPackage struct {
	Metadata struct {
		Titles      []string `xml:"title"`
		Creators    []string `xml:"creator"`
		Description string   `xml:"description"`
		Languages   []string `xml:"language"`
		Identifiers []string `xml:"identifier"`
		Date        string   `xml:"date"`
		Publishers  []string `xml:"publisher"`
		Meta        []struct {
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
			Value    string `xml:",chardata"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
}

func inspectEPUB(req Request, result *Result) error {
	z, err := openArchive(req.FilePath)
	if err != nil {
		return err
	}
	defer z.Close()
	entries := archiveEntries(z.File)
	b := &budget{}
	containerEntry := entries["meta-inf/container.xml"]
	if containerEntry == nil {
		return errors.New("EPUB container is missing")
	}
	data, err := b.read(containerEntry, maxMetadataBytes)
	if err != nil {
		return err
	}
	var container epubContainer
	if err := xml.Unmarshal(data, &container); err != nil {
		return err
	}
	if len(container.Rootfiles) == 0 || !filepath.IsLocal(container.Rootfiles[0].FullPath) {
		return errors.New("EPUB package path is invalid")
	}
	opfPath := path.Clean(container.Rootfiles[0].FullPath)
	opfEntry := entries[strings.ToLower(opfPath)]
	if opfEntry == nil {
		return errors.New("EPUB package is missing")
	}
	opfData, err := b.read(opfEntry, maxMetadataBytes)
	if err != nil {
		return err
	}
	var pkg opfPackage
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return err
	}
	if req.IncludeMetadata {
		applyEPUBMetadata(result.Metadata, pkg)
	}
	if req.IncludeCover {
		coverID := ""
		for _, m := range pkg.Metadata.Meta {
			if strings.EqualFold(strings.TrimSpace(m.Name), "cover") {
				coverID = strings.TrimSpace(m.Content)
			}
		}
		var selected *zip.File
		locator := ""
		for _, item := range pkg.Manifest {
			if containsToken(item.Properties, "cover-image") || (coverID != "" && item.ID == coverID) {
				locator = path.Clean(path.Join(path.Dir(opfPath), item.Href))
				selected = entries[strings.ToLower(locator)]
				if selected != nil {
					break
				}
			}
		}
		if selected == nil {
			for _, f := range z.File {
				if isImage(f.Name) && strings.Contains(strings.ToLower(path.Base(f.Name)), "cover") {
					selected = f
					locator = f.Name
					break
				}
			}
		}
		if selected != nil {
			cover, err := b.read(selected, maxCoverBytes)
			if err != nil {
				return err
			}
			if looksLikeImage(cover) {
				result.Cover = &Cover{Bytes: cover, MIMEType: imageMIME(selected.Name, cover), Locator: locator}
			}
		}
	}
	result.Diagnostics.InspectedBytes = b.used
	return nil
}

func applyEPUBMetadata(out map[string]any, pkg opfPackage) {
	if v := first(pkg.Metadata.Titles); v != "" {
		out["title"] = v
	}
	if v := strings.TrimSpace(pkg.Metadata.Description); v != "" {
		out["description"] = v
	}
	if v := first(pkg.Metadata.Languages); v != "" {
		out["language"] = v
	}
	if values := clean(pkg.Metadata.Creators); len(values) > 0 {
		out["authors"] = values
	}
	if v := first(pkg.Metadata.Publishers); v != "" {
		out["publisher"] = v
	}
	if v := strings.TrimSpace(pkg.Metadata.Date); v != "" {
		out["releaseDate"] = v
	}
	for _, id := range pkg.Metadata.Identifiers {
		n := strings.ToUpper(strings.Map(func(r rune) rune {
			if (r >= '0' && r <= '9') || r == 'X' || r == 'x' {
				return r
			}
			return -1
		}, strings.TrimPrefix(strings.TrimSpace(id), "urn:isbn:")))
		if len(n) == 10 || len(n) == 13 {
			out["isbn"] = n
			break
		}
	}
	out["source"] = "epub"
}

type comicInfo struct {
	Title     string `xml:"Title"`
	Summary   string `xml:"Summary"`
	Writer    string `xml:"Writer"`
	Penciller string `xml:"Penciller"`
	Inker     string `xml:"Inker"`
	Colorist  string `xml:"Colorist"`
	Letterer  string `xml:"Letterer"`
	Publisher string `xml:"Publisher"`
	Year      int    `xml:"Year"`
	Month     int    `xml:"Month"`
	Day       int    `xml:"Day"`
	Genre     string `xml:"Genre"`
	Tags      string `xml:"Tags"`
	PageCount int    `xml:"PageCount"`
	Series    string `xml:"Series"`
	Volume    string `xml:"Volume"`
	Web       string `xml:"Web"`
	Pages     []struct {
		Image int    `xml:"Image,attr"`
		Type  string `xml:"Type,attr"`
	} `xml:"Pages>Page"`
}

func inspectComic(req Request, result *Result) error {
	z, err := openArchive(req.FilePath)
	if err != nil {
		return err
	}
	defer z.Close()
	b := &budget{}
	images := make([]*zip.File, 0)
	var info *zip.File
	for _, f := range z.File {
		if strings.EqualFold(path.Base(f.Name), "ComicInfo.xml") {
			info = f
		}
		if !f.FileInfo().IsDir() && isImage(f.Name) {
			images = append(images, f)
		}
	}
	sort.Slice(images, func(i, j int) bool { return naturalLess(images[i].Name, images[j].Name) })
	var comic comicInfo
	if info != nil {
		data, readErr := b.read(info, maxMetadataBytes)
		if readErr != nil {
			return readErr
		}
		if err := xml.Unmarshal(data, &comic); err != nil {
			return err
		}
		if req.IncludeMetadata {
			applyComicMetadata(result.Metadata, comic)
		}
	}
	if req.IncludeCover && len(images) > 0 {
		candidates := make([]*zip.File, 0, len(images))
		for _, pageInfo := range comic.Pages {
			if strings.EqualFold(strings.TrimSpace(pageInfo.Type), "FrontCover") && pageInfo.Image >= 0 && pageInfo.Image < len(images) {
				candidates = append(candidates, images[pageInfo.Image])
				break
			}
		}
		for _, image := range images {
			if len(candidates) == 0 || image != candidates[0] {
				candidates = append(candidates, image)
			}
		}
		for _, candidate := range candidates {
			data, readErr := b.read(candidate, maxCoverBytes)
			if readErr != nil {
				return readErr
			}
			if looksLikeImage(data) {
				result.Cover = &Cover{Bytes: data, MIMEType: imageMIME(candidate.Name, data), Locator: candidate.Name}
				break
			}
		}
	}
	result.Diagnostics.InspectedBytes = b.used
	return nil
}

func applyComicMetadata(out map[string]any, c comicInfo) {
	put(out, "title", c.Title)
	put(out, "description", c.Summary)
	put(out, "publisher", c.Publisher)
	put(out, "series", c.Series)
	put(out, "volume", c.Volume)
	put(out, "web", c.Web)
	if c.PageCount > 0 {
		out["pageCount"] = c.PageCount
	}
	if c.Year > 0 {
		date := fmt.Sprintf("%04d", c.Year)
		if c.Month > 0 {
			date += fmt.Sprintf("-%02d", c.Month)
		}
		if c.Day > 0 {
			date += fmt.Sprintf("-%02d", c.Day)
		}
		out["releaseDate"] = date
	}
	if v := split(c.Genre); len(v) > 0 {
		out["genres"] = v
	}
	if v := split(c.Tags); len(v) > 0 {
		out["tags"] = v
	}
	authors := []map[string]string{}
	for _, item := range []struct{ name, role string }{{c.Writer, "Writer"}, {c.Penciller, "Penciller"}, {c.Inker, "Inker"}, {c.Colorist, "Colorist"}, {c.Letterer, "Letterer"}} {
		if strings.TrimSpace(item.name) != "" {
			authors = append(authors, map[string]string{"author": strings.TrimSpace(item.name), "role": item.role})
		}
	}
	if len(authors) > 0 {
		out["authors"] = authors
	}
	out["source"] = "comicinfo"
}

func inspectAudio(req Request, result *Result) error {
	f, err := os.Open(req.FilePath)
	if err != nil {
		return err
	}
	defer f.Close()
	m, _ := tag.ReadFrom(f)
	if req.IncludeMetadata {
		if m != nil {
			raw := m.Raw()
			title := first([]string{m.Album(), m.Title()})
			put(result.Metadata, "title", title)
			if a := strings.TrimSpace(m.Artist()); a != "" {
				result.Metadata["authors"] = split(a)
			}
			if n := strings.TrimSpace(m.AlbumArtist()); n != "" && n != strings.TrimSpace(m.Artist()) {
				result.Metadata["narrators"] = split(n)
			} else if n := rawString(raw, "narrator", "performer", "conductor"); n != "" {
				result.Metadata["narrators"] = split(n)
			}
			description := first([]string{m.Comment(), rawString(raw, "description", "summary")})
			put(result.Metadata, "description", description)
			put(result.Metadata, "summary", description)
			put(result.Metadata, "publisher", rawString(raw, "publisher", "organization", "label"))
			put(result.Metadata, "copyright", rawString(raw, "copyright"))
			put(result.Metadata, "language", rawString(raw, "language"))
			if isbn := normalizedISBN(rawString(raw, "isbn")); isbn != "" {
				result.Metadata["isbn"] = isbn
			}
			if m.Year() > 0 {
				result.Metadata["releaseDate"] = strconv.Itoa(m.Year())
			}
			if g := strings.TrimSpace(m.Genre()); g != "" {
				result.Metadata["tags"] = split(g)
			}
			result.Metadata["source"] = "audio_tags"
		}
		if duration := readiumaudio.InspectFileDuration(req.FilePath); duration > 0 {
			result.Metadata["lengthMinutes"] = int(math.Round(duration / 60))
		}
	}
	if req.IncludeCover && m != nil {
		if pic := m.Picture(); pic != nil && len(pic.Data) > 0 && len(pic.Data) <= maxCoverBytes && looksLikeImage(pic.Data) {
			mt := pic.MIMEType
			if mt == "" {
				mt = imageMIME(pic.Ext, pic.Data)
			}
			result.Cover = &Cover{Bytes: pic.Data, MIMEType: mt, Locator: "embedded-picture"}
			result.Diagnostics.InspectedBytes = int64(len(pic.Data))
		}
	}
	return nil
}

func archiveEntries(files []*zip.File) map[string]*zip.File {
	out := make(map[string]*zip.File, len(files))
	for _, f := range files {
		out[strings.ToLower(path.Clean(f.Name))] = f
	}
	return out
}
func containsToken(value, wanted string) bool {
	for _, v := range strings.Fields(value) {
		if v == wanted {
			return true
		}
	}
	return false
}
func first(values []string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
func clean(values []string) []string {
	out := []string{}
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func split(value string) []string {
	return clean(strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == '/' }))
}
func put(out map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		out[key] = value
	}
}
func rawString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		for rawKey, value := range raw {
			if strings.EqualFold(rawKey, key) {
				if text, ok := value.(string); ok {
					if text = strings.TrimSpace(text); text != "" {
						return text
					}
				}
			}
		}
	}
	return ""
}
func normalizedISBN(value string) string {
	normalized := strings.ToUpper(strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == 'x' || r == 'X' {
			return r
		}
		return -1
	}, strings.TrimPrefix(strings.TrimSpace(value), "urn:isbn:")))
	if len(normalized) == 10 || len(normalized) == 13 {
		return normalized
	}
	return ""
}
func isImage(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	}
	return false
}
func looksLikeImage(b []byte) bool {
	return len(b) >= 4 && ((b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff) || (b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4e && b[3] == 0x47) || (len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP") || string(b[:3]) == "GIF")
}
func imageMIME(name string, b []byte) string {
	if detected := mime.TypeByExtension(strings.ToLower(path.Ext(name))); detected != "" {
		return detected
	}
	if len(b) >= 4 && b[0] == 0xff {
		return "image/jpeg"
	}
	if len(b) >= 4 && b[0] == 0x89 {
		return "image/png"
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" {
		return "image/webp"
	}
	return "application/octet-stream"
}
func naturalLess(a, b string) bool {
	aa := strings.ToLower(a)
	bb := strings.ToLower(b)
	for len(aa) > 0 && len(bb) > 0 {
		ad := aa[0] >= '0' && aa[0] <= '9'
		bd := bb[0] >= '0' && bb[0] <= '9'
		if ad && bd {
			ai := 0
			for ai < len(aa) && aa[ai] >= '0' && aa[ai] <= '9' {
				ai++
			}
			bi := 0
			for bi < len(bb) && bb[bi] >= '0' && bb[bi] <= '9' {
				bi++
			}
			an, _ := strconv.ParseUint(aa[:ai], 10, 64)
			bn, _ := strconv.ParseUint(bb[:bi], 10, 64)
			if an != bn {
				return an < bn
			}
			aa = aa[ai:]
			bb = bb[bi:]
			continue
		}
		if aa[0] != bb[0] {
			return aa[0] < bb[0]
		}
		aa = aa[1:]
		bb = bb[1:]
	}
	return len(aa) < len(bb)
}
