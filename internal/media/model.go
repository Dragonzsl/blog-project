package media

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("media not found")
	ErrInUse    = errors.New("media is referenced by content")
)

type Item struct {
	ID             int64
	PublicID       []byte
	PublicIDText   string
	OriginalName   string
	MIMEType       string
	SizeBytes      int64
	Width          int
	Height         int
	ContentHash    []byte
	AltText        string
	ObjectKey      string
	Version        int
	ReferenceCount int
	CreatedAt      time.Time
	Variants       []Variant
}

type Variant struct {
	Key         string
	Width       int
	Height      int
	MIMEType    string
	SizeBytes   int64
	ContentHash []byte
	ObjectKey   string
}

type Asset struct {
	Item        Item
	VariantKey  string
	MIMEType    string
	SizeBytes   int64
	ContentHash []byte
	ObjectKey   string
	CreatedAt   time.Time
}

type ValidationError struct{ Message string }

func (err ValidationError) Error() string { return err.Message }
