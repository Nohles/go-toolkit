package streamer

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
)

const ManifestListPath = "/list.json"

type ManifestSourceType string

const (
	ManifestSourceDirectory ManifestSourceType = "directory"
	ManifestSourceFile      ManifestSourceType = "File"
)

type ManifestListItem struct {
	Manifest     string             `json:"manifest"`
	ManifestType ManifestSourceType `json:"manifestType"`
	DirFile      string             `json:"Dir_File"`
}

type ManifestList struct {
	mu    sync.RWMutex
	items map[string]ManifestListItem
	order []string
}

func NewManifestList(items ...ManifestListItem) *ManifestList {
	list := &ManifestList{
		items: map[string]ManifestListItem{},
	}
	for _, item := range items {
		list.Set(item)
	}
	return list
}

func (l *ManifestList) Set(item ManifestListItem) {
	if l == nil || item.Manifest == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.items == nil {
		l.items = map[string]ManifestListItem{}
	}
	if _, ok := l.items[item.Manifest]; !ok {
		l.order = append(l.order, item.Manifest)
	}
	l.items[item.Manifest] = item
}

func (l *ManifestList) Delete(manifestURL string) {
	if l == nil || manifestURL == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.items, manifestURL)
	l.order = slices.DeleteFunc(l.order, func(v string) bool {
		return v == manifestURL
	})
}

func (l *ManifestList) Items() []ManifestListItem {
	if l == nil {
		return nil
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	items := make([]ManifestListItem, 0, len(l.items))
	for _, manifestURL := range l.order {
		if item, ok := l.items[manifestURL]; ok {
			items = append(items, item)
		}
	}
	return items
}

func (l *ManifestList) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != ManifestListPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(w).Encode(l.Items()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
