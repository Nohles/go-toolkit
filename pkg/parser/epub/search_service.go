package epub

import (
	"github.com/nohles/go-toolkit/pkg/pub"
)

// SearchServiceFactory returns the publication search service factory for
// EPUBs. The factory returns nil when the publication has no searchable
// reading-order resources, so unsupported publications omit the search
// service link entirely rather than advertising a dead end.
func SearchServiceFactory() pub.ServiceFactory {
	return func(context pub.Context, public bool) pub.Service {
		searchable := pub.SearchableReadingOrder(context.Manifest.ReadingOrder)
		if len(searchable) == 0 {
			return nil
		}
		svc := pub.NewCorpusSearchService(context.Manifest.ReadingOrder, context.Fetcher, context.Manifest.TableOfContents)
		svc.SetPublic(public)
		return svc
	}
}
