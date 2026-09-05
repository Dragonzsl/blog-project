package discovery

import (
	"errors"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

var ErrNotFound = errors.New("discovery item not found")

type SearchQuery struct {
	Text         string
	Kind         string
	CategorySlug string
	TagSlug      string
	Sort         string
	Limit        int
	Page         int
	PerPage      int
	Offset       int
}

type SearchResult struct {
	Kind               string
	Path               string
	Title              string
	Excerpt            string
	CoverMediaPublicID []byte
	PublishedAt        time.Time
}

type SearchPage struct {
	Results    []SearchResult
	Pagination pagination.Info
}

type FeedItem struct {
	Path               string
	Title              string
	Excerpt            string
	CoverMediaPublicID []byte
	PublishedAt        time.Time
	UpdatedAt          time.Time
}

type SitemapEntry struct {
	Path         string
	LastModified time.Time
}

// ArchiveMonth is a compact public archive bucket. It intentionally contains
// only aggregate data so the archive index stays cheap to render and cache.
type ArchiveMonth struct {
	Year  int
	Month int
	Count int
}

type ArchiveYear struct {
	Year   int
	Total  int
	Months []ArchiveMonth
}

type ArchivePage struct {
	Year       int
	Month      int
	Results    []SearchResult
	Pagination pagination.Info
}

type Redirect struct {
	TargetPath string
	StatusCode int
}
