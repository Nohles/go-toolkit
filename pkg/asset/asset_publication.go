package asset

import (
	"context"

	"github.com/nohles/go-toolkit/pkg/archive"
	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/mediatype"
)

type Dependencies struct {
	archive.ArchiveFactory
}

// Represents a digital medium (e.g. a file) offering access to a publication.
type PublicationAsset interface {
	Name() string                                                                                              // Name of the asset, e.g. a filename.
	MediaType(ctx context.Context) mediatype.MediaType                                                         // Media type of the asset. If unknown, fallback on `MediaType.Binary`.
	CreateFetcher(ctx context.Context, dependencies Dependencies, credentials string) (fetcher.Fetcher, error) // Creates a fetcher used to access the asset's content.
}
