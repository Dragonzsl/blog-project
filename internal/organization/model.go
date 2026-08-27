package organization

import (
	"errors"
	"time"
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
