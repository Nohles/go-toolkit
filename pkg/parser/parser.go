package parser

import (
	"context"

	"github.com/nohles/go-toolkit/pkg/asset"
	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/pub"
)

type PublicationParser interface {
	Parse(ctx context.Context, asset asset.PublicationAsset, f fetcher.Fetcher) (*pub.Builder, error)
}
