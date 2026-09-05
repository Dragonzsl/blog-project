package media

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

var (
	ErrNotFound = errors.New("media not found")
	ErrInUse    = errors.New("media is referenced by content")
	ErrNotImage = errors.New("media item is not an image")
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

type Page struct {
	Items      []Item
	Pagination pagination.Info
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

// PublicView is the deliberately small media shape that may cross into public
// templates, feeds, or extension responses. It contains URLs and presentation
// metadata only; storage keys, hashes, and database ids stay private.
type PublicView struct {
	URL    string
	Alt    string
	Width  int
	Height int
	SrcSet string
}

func (item Item) PublicView() (PublicView, error) {
	if !strings.HasPrefix(strings.ToLower(item.MIMEType), "image/") {
		return PublicView{}, ErrNotImage
	}
	publicText := item.PublicIDText
	if publicText == "" {
		var err error
		publicText, err = platformid.EncodePublicID(item.PublicID)
		if err != nil {
			return PublicView{}, ErrNotFound
		}
	}
	name := url.PathEscape(item.OriginalName)
	view := PublicView{
		URL:    "/media/" + publicText + "/original/" + name,
		Alt:    item.AltText,
		Width:  item.Width,
		Height: item.Height,
	}
	var sources []string
	for _, variant := range item.Variants {
		if variant.Width < 1 {
			continue
		}
		sources = append(sources, "/media/"+publicText+"/"+variant.Key+"/"+name+" "+strconv.Itoa(variant.Width)+"w")
	}
	if len(sources) > 0 {
		view.SrcSet = strings.Join(sources, ", ")
	}
	return view, nil
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
