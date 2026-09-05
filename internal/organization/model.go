package organization

import (
	"errors"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

var (
	ErrNotFound        = errors.New("organization item not found")
	ErrSlugUnavailable = errors.New("organization slug is already used")
	ErrInvalidTarget   = errors.New("navigation target is invalid")
)

type Category struct {
	ID          int64
	PublicID    []byte
	Slug        string
	Name        string
	Description string
	SortOrder   int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Tag struct {
	ID          int64
	PublicID    []byte
	Slug        string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type PublicCategorySummary struct {
	Category      Category
	ArticleCount  int
	LatestTitle   string
	LatestPath    string
	LatestPublish time.Time
}

type PublicTagSummary struct {
	Tag           Tag
	ArticleCount  int
	LatestTitle   string
	LatestPath    string
	LatestPublish time.Time
}

type NavigationItem struct {
	ID          int64
	Location    string
	ParentID    int64
	Label       string
	TargetKind  string
	TargetID    int64
	ExternalURL string
	URL         string
	SortOrder   int
}

type Redirect struct {
	ID         int64
	SourcePath string
	TargetPath string
	StatusCode int
	Reason     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type RedirectInput struct {
	SourcePath string
	TargetPath string
	StatusCode int
}

type ContentOption struct {
	ID    int64
	Kind  string
	Title string
}

type Taxonomy struct {
	Category *Category
	Tags     []Tag
}

// TaxonomySnapshot is the immutable taxonomy identity stored on a published
// revision. Content list readers use it to batch-resolve terms without
// issuing one query per card.
type TaxonomySnapshot struct {
	ContentID        int64
	CategoryPublicID []byte
	TagPublicIDsJSON string
}

// PublicTaxonomyRebuildState describes the bounded, resumable projection
// rebuild. The projection is derived data; published revisions remain the
// authority when this state is pending or failed.
type PublicTaxonomyRebuildState struct {
	Status          string
	CursorContentID int64
	SchemaVersion   int
	LastError       string
	UpdatedAt       time.Time
}

type PublicCategoryPage struct {
	Category   Category
	ArticleIDs []int64
	Pagination pagination.Info
}

type PublicTagPage struct {
	Tag        Tag
	ArticleIDs []int64
	Pagination pagination.Info
}

type TermInput struct {
	Name        string
	Slug        string
	Description string
	SortOrder   int
}

type NavigationInput struct {
	Location    string
	ParentID    int64
	Label       string
	TargetKind  string
	TargetID    int64
	ExternalURL string
	SortOrder   int
}

type ValidationError struct{ Message string }

func (err ValidationError) Error() string { return err.Message }
