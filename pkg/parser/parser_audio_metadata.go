package parser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/simonhull/audiometa"
)

const audioCoverHref = "~readium/cover"

type audioPublicationEnrichment struct {
	tracks []audioTrackMetadata
	cover  *audioCoverMetadata
}

type audioTrackMetadata struct {
	title       string
	album       string
	subtitle    string
	description string
	language    string
	publisher   string
	authors     []string
	narrators   []string
	genres      []string
	published   *time.Time
	chapters    []audioChapterMetadata
	duration    float64
	bitrate     float64
	size        uint
}

type audioChapterMetadata struct {
	title string
	start time.Duration
	end   time.Duration
}

type audioCoverMetadata struct {
	mediaType mediatype.MediaType
	data      []byte
	width     uint
	height    uint
}

func inspectAudioPublication(ctx context.Context, f fetcher.Fetcher, readingOrder manifest.LinkList) (*audioPublicationEnrichment, error) {
	enrichment := &audioPublicationEnrichment{
		tracks: make([]audioTrackMetadata, len(readingOrder)),
	}

	for i, link := range readingOrder {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		track, cover, err := inspectAudioResource(ctx, f, link)
		enrichment.tracks[i] = track
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			continue
		}
		if enrichment.cover == nil && cover != nil {
			enrichment.cover = cover
		}
	}

	return enrichment, nil
}

func inspectAudioResource(ctx context.Context, f fetcher.Fetcher, link manifest.Link) (audioTrackMetadata, *audioCoverMetadata, error) {
	resource := f.Get(ctx, link)
	defer resource.Close()

	track := audioTrackMetadata{}
	if length, err := resource.Length(ctx); err == nil && length > 0 {
		track.size = uint(length)
	}

	path := resource.File()
	cleanup := func() {}
	if path == "" {
		tmpPath, err := copyAudioResourceToTempFile(ctx, resource, link)
		if err != nil {
			return track, nil, err
		}
		path = tmpPath
		cleanup = func() {
			_ = os.Remove(tmpPath)
		}
	}
	defer cleanup()

	file, err := audiometa.OpenContext(ctx, path)
	if err != nil {
		return track, nil, err
	}
	defer file.Close()

	track = audioTrackMetadataFromFile(file, track.size)

	artwork, err := file.ExtractArtworkContext(ctx)
	if err != nil {
		return track, nil, nil
	}

	return track, selectAudioCover(ctx, artwork), nil
}

func copyAudioResourceToTempFile(ctx context.Context, resource fetcher.Resource, link manifest.Link) (string, error) {
	ext := strings.ToLower(filepath.Ext(link.URL(nil, nil).Path()))
	tmp, err := os.CreateTemp("", "go-toolkit-audio-*"+ext)
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	if _, ex := resource.Stream(ctx, tmp, 0, 0); ex != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", ex
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	return tmpPath, nil
}

func audioTrackMetadataFromFile(file *audiometa.File, size uint) audioTrackMetadata {
	tags := file.Tags
	track := audioTrackMetadata{
		title:       strings.TrimSpace(tags.Title),
		album:       strings.TrimSpace(tags.Album),
		subtitle:    strings.TrimSpace(tags.Subtitle),
		description: firstNonEmpty(tags.Description, tags.Comment),
		language:    strings.TrimSpace(tags.Language),
		publisher:   strings.TrimSpace(tags.Publisher),
		authors:     cleanStrings(firstNonEmptySlice(tags.Artists, splitContributorString(firstNonEmpty(tags.Artist, tags.AlbumArtist)))),
		narrators:   cleanStrings(splitContributorString(tags.Narrator)),
		genres:      cleanStrings(tags.Genres),
		published:   parseAudioPublishedDate(tags.Date, tags.OriginalDate, tags.Year),
		duration:    file.Audio.Duration.Seconds(),
		bitrate:     float64(file.Audio.Bitrate) / 1000,
		size:        size,
	}

	track.chapters = make([]audioChapterMetadata, 0, len(file.Chapters))
	for _, chapter := range file.Chapters {
		title := strings.TrimSpace(chapter.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", chapter.Index)
		}
		track.chapters = append(track.chapters, audioChapterMetadata{
			title: title,
			start: chapter.StartTime,
			end:   chapter.EndTime,
		})
	}

	return track
}

func selectAudioCover(ctx context.Context, artwork []audiometa.Artwork) *audioCoverMetadata {
	if len(artwork) == 0 {
		return nil
	}

	best := artwork[0]
	for _, image := range artwork {
		if image.Type == audiometa.ArtworkFrontCover {
			best = image
			break
		}
	}
	if len(best.Data) == 0 {
		return nil
	}

	mt, err := mediatype.NewOfString(best.MIMEType)
	if err != nil || !mt.IsBitmap() {
		if inferred := mediatype.OfBytesOnly(ctx, best.Data); inferred != nil && inferred.IsBitmap() {
			mt = *inferred
		} else {
			return nil
		}
	}

	return &audioCoverMetadata{
		mediaType: mt,
		data:      slices.Clone(best.Data),
		width:     uint(max(best.Width, 0)),
		height:    uint(max(best.Height, 0)),
	}
}

func (e audioPublicationEnrichment) enrichReadingOrder(readingOrder manifest.LinkList) manifest.LinkList {
	for i := range readingOrder {
		if i >= len(e.tracks) {
			break
		}
		track := e.tracks[i]
		if track.title != "" {
			readingOrder[i].Title = track.title
		}
		if track.duration > 0 {
			readingOrder[i].Duration = track.duration
		}
		if track.bitrate > 0 {
			readingOrder[i].Bitrate = track.bitrate
		}
		if track.size > 0 {
			readingOrder[i].Size = track.size
		}
	}
	return readingOrder
}

func (e audioPublicationEnrichment) enrichMetadata(metadata *manifest.Metadata, fallbackTitle string, trackCount int) {
	title := firstAlbumTitle(e.tracks)
	if title == "" && trackCount == 1 {
		title = firstTrackTitle(e.tracks)
	}
	if title == "" {
		title = fallbackTitle
	}
	metadata.LocalizedTitle = manifest.NewLocalizedStringFromString(title)

	if subtitle := firstString(e.tracks, func(track audioTrackMetadata) string { return track.subtitle }); subtitle != "" {
		metadata.LocalizedSubtitle = localizedStringPointer(subtitle)
	}
	if description := firstString(e.tracks, func(track audioTrackMetadata) string { return track.description }); description != "" {
		metadata.Description = description
	}
	if language := firstString(e.tracks, func(track audioTrackMetadata) string { return track.language }); language != "" {
		metadata.Languages = manifest.Strings{language}
	}
	if publisher := firstString(e.tracks, func(track audioTrackMetadata) string { return track.publisher }); publisher != "" {
		metadata.Publishers = contributorsFromNames([]string{publisher})
	}
	if authors := firstStringSlice(e.tracks, func(track audioTrackMetadata) []string { return track.authors }); len(authors) > 0 {
		metadata.Authors = contributorsFromNames(authors)
	}
	if narrators := firstStringSlice(e.tracks, func(track audioTrackMetadata) []string { return track.narrators }); len(narrators) > 0 {
		metadata.Narrators = contributorsFromNames(narrators)
	}
	if genres := firstStringSlice(e.tracks, func(track audioTrackMetadata) []string { return track.genres }); len(genres) > 0 {
		metadata.Subjects = subjectsFromNames(genres)
	}
	if published := firstPublishedDate(e.tracks); published != nil {
		metadata.Published = published
	}
	if duration := totalTrackDuration(e.tracks); duration > 0 {
		metadata.Duration = &duration
	}
}

func (e audioPublicationEnrichment) tableOfContents(readingOrder manifest.LinkList) manifest.LinkList {
	toc := make(manifest.LinkList, 0, len(readingOrder))
	for i, link := range readingOrder {
		if i >= len(e.tracks) {
			break
		}
		track := e.tracks[i]
		if len(track.chapters) > 0 {
			for _, chapter := range track.chapters {
				toc = append(toc, manifest.Link{
					Href:  manifest.MustNewHREFFromString(audioHrefWithTimestamp(link, chapter.start), false),
					Title: chapter.title,
				})
			}
			continue
		}

		title := firstNonEmpty(link.Title, titleFromAudioHref(link))
		if title == "" {
			continue
		}
		toc = append(toc, manifest.Link{
			Href:  manifest.MustNewHREFFromString(audioHrefWithTimestamp(link, 0), false),
			Title: title,
		})
	}
	return toc
}

func (e audioPublicationEnrichment) coverLink() *manifest.Link {
	if e.cover == nil {
		return nil
	}
	return &manifest.Link{
		Href:      manifest.MustNewHREFFromString(audioCoverHref, false),
		MediaType: &e.cover.mediaType,
		Rels:      manifest.Strings{"cover"},
		Width:     e.cover.width,
		Height:    e.cover.height,
		Size:      uint(len(e.cover.data)),
	}
}

func (e audioPublicationEnrichment) coverServiceFactory() pub.ServiceFactory {
	cover := e.cover
	link := *e.coverLink()
	return func(context pub.Context, public bool) pub.Service {
		return audioCoverService{
			link: link,
			data: slices.Clone(cover.data),
		}
	}
}

type audioCoverService struct {
	link manifest.Link
	data []byte
}

func (s audioCoverService) Links() manifest.LinkList {
	return nil
}

func (s audioCoverService) Get(ctx context.Context, link manifest.Link) (fetcher.Resource, bool) {
	if !s.link.URL(nil, nil).Equivalent(link.URL(nil, nil)) {
		return nil, false
	}
	return fetcher.NewBytesResource(s.link, func() []byte {
		return slices.Clone(s.data)
	}), true
}

func (s audioCoverService) Close() {}

func audioHrefWithTimestamp(link manifest.Link, timestamp time.Duration) string {
	base := link.URL(nil, nil).RemoveFragment().String()
	return base + "#t=" + formatAudioTimestamp(timestamp)
}

func formatAudioTimestamp(timestamp time.Duration) string {
	seconds := timestamp.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}

func titleFromAudioHref(link manifest.Link) string {
	title := link.URL(nil, nil).Filename()
	ext := filepath.Ext(title)
	if ext != "" {
		title = strings.TrimSuffix(title, ext)
	}
	return strings.TrimSpace(title)
}

func firstAlbumTitle(tracks []audioTrackMetadata) string {
	return firstString(tracks, func(track audioTrackMetadata) string { return track.album })
}

func firstTrackTitle(tracks []audioTrackMetadata) string {
	return firstString(tracks, func(track audioTrackMetadata) string { return track.title })
}

func firstString(tracks []audioTrackMetadata, getter func(audioTrackMetadata) string) string {
	for _, track := range tracks {
		if value := strings.TrimSpace(getter(track)); value != "" {
			return value
		}
	}
	return ""
}

func firstStringSlice(tracks []audioTrackMetadata, getter func(audioTrackMetadata) []string) []string {
	for _, track := range tracks {
		if values := cleanStrings(getter(track)); len(values) > 0 {
			return values
		}
	}
	return nil
}

func firstPublishedDate(tracks []audioTrackMetadata) *time.Time {
	for _, track := range tracks {
		if track.published != nil {
			published := *track.published
			return &published
		}
	}
	return nil
}

func totalTrackDuration(tracks []audioTrackMetadata) float64 {
	var duration float64
	for _, track := range tracks {
		if track.duration > 0 {
			duration += track.duration
		}
	}
	return duration
}

func contributorsFromNames(names []string) manifest.Contributors {
	names = cleanStrings(names)
	contributors := make(manifest.Contributors, 0, len(names))
	for _, name := range names {
		contributors = append(contributors, manifest.Contributor{
			LocalizedName: manifest.NewLocalizedStringFromString(name),
		})
	}
	return contributors
}

func subjectsFromNames(names []string) []manifest.Subject {
	names = cleanStrings(names)
	subjects := make([]manifest.Subject, 0, len(names))
	for _, name := range names {
		subjects = append(subjects, manifest.Subject{
			LocalizedName: manifest.NewLocalizedStringFromString(name),
		})
	}
	return subjects
}

func localizedStringPointer(value string) *manifest.LocalizedString {
	ls := manifest.NewLocalizedStringFromString(value)
	return &ls
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptySlice(values ...[]string) []string {
	for _, value := range values {
		if clean := cleanStrings(value); len(clean) > 0 {
			return clean
		}
	}
	return nil
}

func splitContributorString(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == '|'
	})
}

func cleanStrings(values []string) []string {
	clean := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		clean = append(clean, value)
	}
	return clean
}

func parseAudioPublishedDate(values ...interface{}) *time.Time {
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if parsed := parseAudioPublishedDateString(v); parsed != nil {
				return parsed
			}
		case int:
			if v > 0 {
				published := time.Date(v, time.January, 1, 0, 0, 0, 0, time.UTC)
				return &published
			}
		}
	}
	return nil
}

func parseAudioPublishedDateString(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01", "2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return nil
}
