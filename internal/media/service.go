package media

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/image/draw"

	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

type Options struct {
	MaxUploadBytes int64
	MaxImagePixels int
	VariantWidths  []int
	JPEGQuality    int
}

func DefaultOptions() Options {
	return Options{MaxUploadBytes: 12 << 20, MaxImagePixels: 16_000_000, VariantWidths: []int{640, 1280}, JPEGQuality: 92}
}

type Service struct {
	repository  *Repository
	root        string
	storage     Storage
	logger      *slog.Logger
	variantGate chan struct{}
	options     Options
	now         func() time.Time
}

func NewService(db *database.DB, root string, logger *slog.Logger, configured ...Options) (*Service, error) {
	storage, err := NewLocalStorage(root)
	if err != nil {
		return nil, err
	}
	return NewServiceWithStorage(db, root, storage, logger, configured...)
}

func NewServiceWithStorage(db *database.DB, root string, storage Storage, logger *slog.Logger, configured ...Options) (*Service, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create media directory: %w", err)
	}
	if storage == nil {
		return nil, fmt.Errorf("media storage is required")
	}
	options := DefaultOptions()
	if len(configured) > 0 {
		options = configured[0]
	}
	options.VariantWidths = append([]int(nil), options.VariantWidths...)
	return &Service{repository: NewRepository(db), root: root, storage: storage, logger: logger, variantGate: make(chan struct{}, 1), options: options, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) MaxUploadBytes() int64 { return s.options.MaxUploadBytes }

func (s *Service) Upload(ctx context.Context, originalName, altText string, source multipart.File) (item Item, err error) {
	name, err := safeName(originalName)
	if err != nil {
		return Item{}, err
	}
	altText = strings.TrimSpace(altText)
	if !utf8.ValidString(altText) || utf8.RuneCountInString(altText) > 500 {
		return Item{}, ValidationError{Message: "替代文字不能超过 500 个字符"}
	}
	temporary, err := os.CreateTemp(s.root, ".upload-*.tmp")
	if err != nil {
		return Item{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(source, s.options.MaxUploadBytes+1))
	if err != nil {
		temporary.Close()
		return Item{}, fmt.Errorf("store upload: %w", err)
	}
	if written > s.options.MaxUploadBytes {
		temporary.Close()
		return Item{}, ValidationError{Message: fmt.Sprintf("单个文件不能超过 %.1f MiB", float64(s.options.MaxUploadBytes)/(1<<20))}
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return Item{}, err
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		temporary.Close()
		return Item{}, err
	}
	header := make([]byte, 512)
	headerBytes, _ := io.ReadFull(temporary, header)
	header = header[:headerBytes]
	mimeType := http.DetectContentType(header)
	extension, isImage, err := allowedType(mimeType, header)
	if err != nil {
		temporary.Close()
		return Item{}, err
	}
	width, height := 0, 0
	if isImage {
		if _, err := temporary.Seek(0, io.SeekStart); err != nil {
			temporary.Close()
			return Item{}, err
		}
		configuration, _, err := image.DecodeConfig(bufio.NewReader(temporary))
		if err != nil {
			temporary.Close()
			return Item{}, ValidationError{Message: "图片无法解码"}
		}
		width, height = configuration.Width, configuration.Height
		if width < 1 || height < 1 || int64(width)*int64(height) > int64(s.options.MaxImagePixels) {
			temporary.Close()
			return Item{}, ValidationError{Message: fmt.Sprintf("图片像素超过 %d 万上限", s.options.MaxImagePixels/10_000)}
		}
	}
	if err := temporary.Close(); err != nil {
		return Item{}, err
	}
	publicID, err := platformid.NewPublicID(s.now())
	if err != nil {
		return Item{}, err
	}
	publicText, err := platformid.EncodePublicID(publicID)
	if err != nil {
		return Item{}, err
	}
	directory := s.directory(publicText)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Item{}, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	var uploadedKeys []string
	defer func() {
		if err == nil || s.storage.Name() == "local" {
			return
		}
		for _, key := range uploadedKeys {
			_ = s.storage.Delete(context.Background(), key)
		}
	}()
	objectKey := filepath.ToSlash(filepath.Join(publicText[:2], publicText, "original"+extension))
	originalPath := filepath.Join(s.root, filepath.FromSlash(objectKey))
	if err = os.Rename(temporaryPath, originalPath); err != nil {
		return Item{}, fmt.Errorf("publish media original: %w", err)
	}
	item = Item{PublicID: publicID, PublicIDText: publicText, OriginalName: name, MIMEType: mimeType, SizeBytes: written, Width: width, Height: height, ContentHash: hasher.Sum(nil), AltText: altText, ObjectKey: objectKey, Version: 1, CreatedAt: s.now()}
	if isImage {
		select {
		case s.variantGate <- struct{}{}:
		case <-ctx.Done():
			return Item{}, ctx.Err()
		}
		item.Variants, err = s.generateVariants(ctx, item, extension)
		<-s.variantGate
		debug.FreeOSMemory()
		if err != nil {
			return Item{}, err
		}
	}
	if s.storage.Name() != "local" {
		if err = s.putFile(ctx, item.ObjectKey, originalPath, item.SizeBytes); err != nil {
			return Item{}, fmt.Errorf("store media original in %s: %w", s.storage.Name(), err)
		}
		uploadedKeys = append(uploadedKeys, item.ObjectKey)
		for _, variant := range item.Variants {
			variantPath := filepath.Join(s.root, filepath.FromSlash(variant.ObjectKey))
			if err = s.putFile(ctx, variant.ObjectKey, variantPath, variant.SizeBytes); err != nil {
				return Item{}, fmt.Errorf("store media variant in %s: %w", s.storage.Name(), err)
			}
			uploadedKeys = append(uploadedKeys, variant.ObjectKey)
		}
	}
	item, err = s.repository.Create(ctx, item, s.now())
	if err != nil {
		return Item{}, err
	}
	if err := s.recordStorageLocations(ctx, item); err != nil {
		// The media row and its location index form one logical write.  If the
		// second transaction cannot be committed, remove the orphan row while
		// the deferred cleanup removes local/remote objects.
		_, _ = s.repository.Delete(ctx, item.ID, s.now())
		return Item{}, err
	}
	item.PublicIDText = publicText
	return item, nil
}

func (s *Service) recordStorageLocations(ctx context.Context, item Item) error {
	now := s.now().UnixMilli()
	tx, err := s.repository.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_storage_locations(media_id,variant_key,adapter,object_key,size_bytes,content_hash,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(media_id,variant_key) DO UPDATE SET adapter=excluded.adapter,object_key=excluded.object_key,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,updated_at=excluded.updated_at`, item.ID, "original", s.storage.Name(), item.ObjectKey, item.SizeBytes, item.ContentHash, now, now); err != nil {
		return err
	}
	for _, variant := range item.Variants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_storage_locations(media_id,variant_key,adapter,object_key,size_bytes,content_hash,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(media_id,variant_key) DO UPDATE SET adapter=excluded.adapter,object_key=excluded.object_key,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,updated_at=excluded.updated_at`, item.ID, variant.Key, s.storage.Name(), variant.ObjectKey, variant.SizeBytes, variant.ContentHash, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) Items(ctx context.Context) ([]Item, error) {
	items, err := s.repository.Items(ctx)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].PublicIDText, _ = platformid.EncodePublicID(items[index].PublicID)
	}
	return items, nil
}

func (s *Service) ItemsPage(ctx context.Context, request pagination.Request) (Page, error) {
	page, err := s.repository.ItemsPage(ctx, request)
	if err != nil {
		return Page{}, err
	}
	for index := range page.Items {
		page.Items[index].PublicIDText, _ = platformid.EncodePublicID(page.Items[index].PublicID)
	}
	return page, nil
}

// PublicItem resolves an immutable media public id for public rendering. The
// returned item is still an internal value; callers should convert it with
// Item.PublicView before exposing it outside the media module.
func (s *Service) PublicItem(ctx context.Context, publicID []byte) (Item, error) {
	item, err := s.repository.ItemByPublicID(ctx, publicID)
	if err != nil {
		return Item{}, err
	}
	item.PublicIDText, err = platformid.EncodePublicID(item.PublicID)
	if err != nil {
		return Item{}, ErrNotFound
	}
	return item, nil
}

func (s *Service) PublicItems(ctx context.Context, publicIDs [][]byte) ([]Item, error) {
	items, err := s.repository.ItemsByPublicIDs(ctx, publicIDs)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].PublicIDText, _ = platformid.EncodePublicID(items[index].PublicID)
	}
	return items, nil
}

func (s *Service) ItemByPublicID(ctx context.Context, publicID []byte) (Item, error) {
	return s.PublicItem(ctx, publicID)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	existing, err := s.repository.Item(ctx, id)
	if err != nil {
		return err
	}
	item, err := s.repository.Delete(ctx, id, s.now())
	if err != nil {
		return err
	}
	publicText, _ := platformid.EncodePublicID(item.PublicID)
	directory := s.directory(publicText)
	if err := os.RemoveAll(directory); err != nil {
		s.logger.ErrorContext(ctx, "remove deleted media files", "error", err, "media_id", publicText)
	}
	if s.storage.Name() != "local" {
		_ = s.storage.Delete(ctx, item.ObjectKey)
		for _, variant := range existing.Variants {
			_ = s.storage.Delete(ctx, variant.ObjectKey)
		}
	}
	return nil
}

func (s *Service) Asset(ctx context.Context, publicIDText, variant string) (Asset, error) {
	publicID, err := platformid.DecodePublicID(publicIDText)
	if err != nil {
		return Asset{}, ErrNotFound
	}
	return s.repository.Asset(ctx, publicID, variant)
}

type ReadSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

func (s *Service) Open(asset Asset) (ReadSeekCloser, error) {
	path := filepath.Join(s.root, filepath.FromSlash(asset.ObjectKey))
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, ErrNotFound
	}
	if file, err := os.Open(path); err == nil {
		return file, nil
	} else if s.storage.Name() == "local" {
		return nil, err
	}
	remote, err := s.storage.Open(context.Background(), asset.ObjectKey)
	if err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(s.root, ".remote-*.tmp")
	if err != nil {
		remote.Close()
		return nil, err
	}
	if _, err := io.Copy(temporary, io.LimitReader(remote, asset.SizeBytes+1)); err != nil {
		remote.Close()
		temporary.Close()
		os.Remove(temporary.Name())
		return nil, err
	}
	remote.Close()
	if err := temporary.Close(); err != nil {
		os.Remove(temporary.Name())
		return nil, err
	}
	file, err := os.Open(temporary.Name())
	if err != nil {
		os.Remove(temporary.Name())
		return nil, err
	}
	return &managedFile{File: file, path: temporary.Name()}, nil
}

type managedFile struct {
	*os.File
	path string
}

func (f *managedFile) Close() error {
	err := f.File.Close()
	_ = os.Remove(f.path)
	return err
}

func (s *Service) putFile(ctx context.Context, key, file string, size int64) error {
	input, err := os.Open(file)
	if err != nil {
		return err
	}
	defer input.Close()
	return s.storage.Put(ctx, key, input, size)
}

func (s *Service) generateVariants(ctx context.Context, item Item, extension string) ([]Variant, error) {
	source, err := os.Open(filepath.Join(s.root, filepath.FromSlash(item.ObjectKey)))
	if err != nil {
		return nil, err
	}
	decoded, _, err := image.Decode(bufio.NewReader(source))
	source.Close()
	if err != nil {
		return nil, ValidationError{Message: "图片无法解码"}
	}
	var variants []Variant
	for _, width := range s.options.VariantWidths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.Width <= width {
			continue
		}
		height := max(1, int(float64(item.Height)*float64(width)/float64(item.Width)))
		destination := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.CatmullRom.Scale(destination, destination.Bounds(), decoded, decoded.Bounds(), draw.Over, nil)
		key := fmt.Sprintf("w%d", width)
		objectKey := filepath.ToSlash(filepath.Join(item.PublicIDText[:2], item.PublicIDText, key+extension))
		temporary, err := os.CreateTemp(s.directory(item.PublicIDText), ".variant-*.tmp")
		if err != nil {
			return nil, err
		}
		temporaryPath := temporary.Name()
		hash := sha256.New()
		writer := io.MultiWriter(temporary, hash)
		if item.MIMEType == "image/jpeg" {
			err = jpeg.Encode(writer, destination, &jpeg.Options{Quality: s.options.JPEGQuality})
		} else {
			encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
			err = encoder.Encode(writer, destination)
		}
		if err == nil {
			err = temporary.Sync()
		}
		closeErr := temporary.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(temporaryPath)
			return nil, fmt.Errorf("encode media variant: %w", err)
		}
		info, err := os.Stat(temporaryPath)
		if err != nil {
			os.Remove(temporaryPath)
			return nil, err
		}
		if err := os.Rename(temporaryPath, filepath.Join(s.root, filepath.FromSlash(objectKey))); err != nil {
			os.Remove(temporaryPath)
			return nil, err
		}
		variants = append(variants, Variant{Key: key, Width: width, Height: height, MIMEType: item.MIMEType, SizeBytes: info.Size(), ContentHash: hash.Sum(nil), ObjectKey: objectKey})
	}
	return variants, nil
}

func (s *Service) directory(publicID string) string {
	return filepath.Join(s.root, publicID[:2], publicID)
}
func safeName(value string) (string, error) {
	value = filepath.Base(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 {
		return "", ValidationError{Message: "文件名无效"}
	}
	return value, nil
}
func allowedType(mimeType string, header []byte) (string, bool, error) {
	switch mimeType {
	case "image/jpeg":
		return ".jpg", true, nil
	case "image/png":
		return ".png", true, nil
	case "application/pdf":
		return ".pdf", false, nil
	case "application/zip":
		return ".zip", false, nil
	case "text/plain; charset=utf-8", "text/plain; charset=us-ascii":
		lower := strings.ToLower(string(header))
		if strings.Contains(lower, "<svg") {
			return "", false, ValidationError{Message: "SVG 默认不允许上传"}
		}
		return ".txt", false, nil
	default:
		return "", false, ValidationError{Message: "仅支持 JPEG、PNG、PDF、纯文本或 ZIP 文件"}
	}
}

func (item Item) OriginalURL() string {
	return "/media/" + item.PublicIDText + "/original/" + url.PathEscape(item.OriginalName)
}
func (item Item) VariantURL(key string) string {
	return "/media/" + item.PublicIDText + "/" + key + "/" + url.PathEscape(item.OriginalName)
}
