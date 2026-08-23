package publishing

import (
	"errors"
	"time"

	"github.com/zhushilin/blog-project/internal/organization"
)

var (
	ErrNotFound               = errors.New("content not found")
	ErrConflict               = errors.New("content was changed by another editor")
	ErrSlugUnavailable        = errors.New("slug is already reserved")
	ErrPublishedSlugImmutable = errors.New("published permalink cannot be changed")
)

type Article struct {
	ID                        int64
	PublicID                  []byte
	Kind                      string
	Status                    string
	Slug                      string
	Title                     string
	Excerpt                   string
	BodyMarkdown              string
	CurrentRevisionID         int64
	PublishedRevisionID       int64
	PublishedAt               *time.Time
	PublishedRevisionAt       *time.Time
	LockVersion               int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	Category                  *organization.Category
	Tags                      []organization.Tag
	publishedCategoryPublicID []byte
	publishedTagPublicIDsJSON string
}

type DraftInput struct {
	Title        string
	Slug         string
	Excerpt      string
	BodyMarkdown string
	CategoryID   int64
	TagIDs       []int64
	slugKey      string
}

type ValidationError struct {
	Message string
}

func (err ValidationError) Error() string { return err.Message }

type revisionInput struct {
	PublicID         []byte
	Title            string
	Slug             string
	SlugKey          string
	Excerpt          string
	BodyMarkdown     string
	CategoryPublicID []byte
	TagPublicIDsJSON string
	Reason           string
}
