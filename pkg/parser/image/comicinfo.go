package image

import (
	"context"
	"encoding/xml"
	"path"
	"strings"

	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/manifest"
)

// maxComicInfoBytes caps how much of a ComicInfo.xml is read. Real files are a
// few kilobytes; anything far larger is not worth parsing during a publication
// open.
const maxComicInfoBytes = 1 << 20

// comicInfo holds the ComicInfo.xml fields that change how a comic is read.
// See https://anansi-project.github.io/docs/comicinfo/documentation.
type comicInfo struct {
	Manga string `xml:"Manga"`
	Pages []struct {
		Image      int    `xml:"Image,attr"`
		DoublePage string `xml:"DoublePage,attr"`
	} `xml:"Pages>Page"`
}

// rightToLeft reports whether ComicInfo marks the comic as manga read right to
// left. A plain "Yes" only says the comic is manga, so it does not set a
// direction.
func (c comicInfo) rightToLeft() bool {
	return strings.EqualFold(strings.TrimSpace(c.Manga), "YesAndRightToLeft")
}

// doublePageIndices returns the reading-order indices ComicInfo marks as a
// two-page spread stored in one image.
func (c comicInfo) doublePageIndices() map[int]struct{} {
	indices := map[int]struct{}{}
	for _, page := range c.Pages {
		if page.Image >= 0 && strings.EqualFold(strings.TrimSpace(page.DoublePage), "true") {
			indices[page.Image] = struct{}{}
		}
	}
	return indices
}

// findComicInfoLink returns the shallowest ComicInfo.xml in the publication, so
// a file at the archive root wins over one nested in a folder.
func findComicInfoLink(links manifest.LinkList) (manifest.Link, bool) {
	var found manifest.Link
	foundDepth := -1
	for _, link := range links {
		p := link.URL(nil, nil).Path()
		if !strings.EqualFold(path.Base(p), "ComicInfo.xml") {
			continue
		}
		depth := strings.Count(strings.Trim(p, "/"), "/")
		if foundDepth == -1 || depth < foundDepth {
			found = link
			foundDepth = depth
		}
	}
	return found, foundDepth >= 0
}

// readComicInfo reads and parses the publication's ComicInfo.xml. A missing,
// oversized or malformed file yields ok=false; it never fails parsing.
func readComicInfo(ctx context.Context, f fetcher.Fetcher, links manifest.LinkList) (comicInfo, bool) {
	link, ok := findComicInfoLink(links)
	if !ok {
		return comicInfo{}, false
	}
	resource := f.Get(ctx, link)
	defer resource.Close()
	if length, err := resource.Length(ctx); err == nil && length > maxComicInfoBytes {
		return comicInfo{}, false
	}
	data, rerr := resource.Read(ctx, 0, maxComicInfoBytes-1)
	if rerr != nil || len(data) == 0 {
		return comicInfo{}, false
	}
	var info comicInfo
	if err := xml.Unmarshal(data, &info); err != nil {
		return comicInfo{}, false
	}
	return info, true
}

// applyComicInfo copies reading hints from ComicInfo.xml into the manifest:
// right-to-left manga sets the reading progression, and pages marked
// DoublePage get the `page: center` property, which tells a reader that shows
// two pages side by side to show that page on its own.
//
// pageIndexed is false when the reading order is not one entry per page (a
// folder of chapter archives), so ComicInfo page indices do not apply.
func applyComicInfo(m *manifest.Manifest, info comicInfo, pageIndexed bool) {
	if info.rightToLeft() && m.Metadata.ReadingProgression == manifest.None {
		m.Metadata.ReadingProgression = manifest.RTL
	}
	if !pageIndexed {
		return
	}
	for index := range info.doublePageIndices() {
		if index >= len(m.ReadingOrder) {
			continue
		}
		link := &m.ReadingOrder[index]
		if link.Properties.Page() != manifest.PageNone {
			continue
		}
		// Properties are treated as immutable and may be shared, so copy.
		properties := make(manifest.Properties, len(link.Properties)+1)
		for key, value := range link.Properties {
			properties[key] = value
		}
		properties["page"] = string(manifest.PageCenter)
		link.Properties = properties
	}
}
