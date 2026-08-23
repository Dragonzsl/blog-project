package discovery

import "time"

type SearchQuery struct {
	Text         string
	Kind         string
	CategorySlug string
	TagSlug      string
	Sort         string
	Limit        int
}

type SearchResult struct {
	Kind        string
	Path        string
	Title       string
	Excerpt     string
	PublishedAt time.Time
}

type FeedItem struct {
	Path        string
	Title       string
	Excerpt     string
	PublishedAt time.Time
	UpdatedAt   time.Time
}

type SitemapEntry struct {
	Path         string
	LastModified time.Time
}

type Redirect struct {
	TargetPath string
	StatusCode int
}
