package audio

import (
	"context"
	"path/filepath"

	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/manifest"
)

// InspectFileDuration reads only the ranges required to determine a local
// audio file's duration. It never enumerates or extracts embedded chapters.
func InspectFileDuration(filePath string) float64 {
	link := manifest.Link{
		Href: manifest.MustNewHREFFromString(filepath.Base(filePath), false),
	}
	resource := fetcher.NewFileResource(link, filePath)
	defer resource.Close()
	return probeAudioFile(
		context.Background(),
		resource,
		link,
		nil,
		false,
		1,
	).Duration
}
