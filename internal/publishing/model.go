package publishing

import (
	"errors"
	"time"

	"github.com/zhushilin/blog-project/internal/organization"
)

var (
	ErrNotFound          = errors.New("content not found")
	ErrConflict          = errors.New("content was changed by another editor")
	ErrSlugUnavailable   = errors.New("slug is already reserved")
	ErrInvalidTransition = errors.New("content lifecycle transition is not allowed")
)

type Article struct {
	ID                        int64
	PublicID                  []byte
	Kind                      string
	Status                    string
	Slug                      string
	PublishedSlug             string
	Title                     string
	Excerpt                   string
	SEOTitle                  string
	SEODescription            string
	BodyMarkdown              string
	CurrentRevisionID         int64
	PublishedRevisionID       int64
	PublishedAt               *time.Time
	PublishedRevisionAt       *time.Time
	ScheduledAt               *time.Time
	WithdrawnAt               *time.Time
	TrashedAt                 *time.Time
	LockVersion               int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	Category                  *organization.Category
	Tags                      []organization.Tag
	publishedCategoryPublicID []byte
	publishedTagPublicIDsJSON string
}

type Revision struct {
	ID                      int64
	PublicID                []byte
	ContentID               int64
	Number                  int64
	Title                   string
	Slug                    string
	Excerpt                 string
	SEOTitle                string
	SEODescription          string
	BodyMarkdown            string
	CategoryPublicID        []byte
	TagPublicIDsJSON        string
	Reason                  string
	IsPublicationCheckpoint bool
	CreatedAt               time.Time
}

type EditingSnapshot struct {
	ContentID       int64
	BaseLockVersion int64
	BrowserVersion  int64
	Input           DraftInput
	UpdatedAt       time.Time
}

type DraftInput struct {
	Title          string
	Slug           string
	Excerpt        string
	SEOTitle       string
	SEODescription string
	BodyMarkdown   string
	CategoryID     int64
	TagIDs         []int64
	slugKey        string
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
	SEOTitle         string
	SEODescription   string
	BodyMarkdown     string
	CategoryPublicID []byte
	TagPublicIDsJSON string
	Reason           string
}
