package media_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

type mediaSecurity struct{}

func (mediaSecurity) CSRFToken(*http.Request) string                           { return "csrf" }
func (mediaSecurity) VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool { return true }

type mediaSite struct{}

func (mediaSite) SiteName(context.Context) (string, error) { return "媒体测试", nil }

func TestUploadPreservesOriginalGeneratesVariantsAndProtectsReferences(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := filepath.Join(t.TempDir(), "media")
	service, err := media.NewService(db, root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	imageBuffer := new(bytes.Buffer)
	sourceImage := image.NewRGBA(image.Rect(0, 0, 1400, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 1400; x++ {
			sourceImage.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 110, A: 255})
		}
	}
	if err := jpeg.Encode(imageBuffer, sourceImage, &jpeg.Options{Quality: 98}); err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), imageBuffer.Bytes()...)
	temporary, err := os.CreateTemp(t.TempDir(), "upload-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := temporary.Write(original); err != nil {
		t.Fatal(err)
	}
	if _, err := temporary.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	item, err := service.Upload(ctx, "花园 原图.jpg", "绿色花园", temporary)
	temporary.Close()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(item.ObjectKey)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, original) {
		t.Fatal("original upload bytes were rewritten")
	}
	if len(item.Variants) != 2 || item.Variants[0].Width != 640 || item.Variants[1].Width != 1280 {
		t.Fatalf("variants=%+v", item.Variants)
	}
	for _, variant := range item.Variants {
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(variant.ObjectKey)))
		if err != nil {
			t.Fatal(err)
		}
		configuration, _, err := image.DecodeConfig(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if configuration.Width != variant.Width || configuration.Height != variant.Height {
			t.Fatalf("variant %s dimensions=%dx%d", variant.Key, configuration.Width, configuration.Height)
		}
	}
	handler, err := media.NewHTTPHandler(service, mediaSecurity{}, mediaSite{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	handler.RegisterPublic(router)
	router.Route("/admin", handler.RegisterAdmin)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, item.OriginalURL(), nil))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), original) || !strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset status=%d cache=%q", response.Code, response.Header().Get("Cache-Control"))
	}
	conditionalRequest := httptest.NewRequest(http.MethodGet, item.OriginalURL(), nil)
	conditionalRequest.Header.Set("If-None-Match", response.Header().Get("ETag"))
	conditional := httptest.NewRecorder()
	router.ServeHTTP(conditional, conditionalRequest)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional status=%d body=%d", conditional.Code, conditional.Body.Len())
	}
	admin := httptest.NewRecorder()
	router.ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/admin/media", nil))
	if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), "花园 原图.jpg") || !strings.Contains(admin.Body.String(), item.OriginalURL()) {
		t.Fatalf("admin media status=%d body=%s", admin.Code, admin.Body.String())
	}
	publishingService := publishing.NewService(publishing.NewRepository(db))
	article, err := publishingService.CreateDraft(ctx, publishing.DraftInput{Title: "媒体引用", Slug: "media-reference", BodyMarkdown: "![图](" + item.OriginalURL() + ")"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, item.ID); !errors.Is(err, media.ErrInUse) {
		t.Fatalf("delete referenced media error=%v", err)
	}
	article, err = publishingService.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	article, err = publishingService.UpdateDraft(ctx, article.ID, article.LockVersion, publishing.DraftInput{Title: "媒体引用", Slug: "media-reference", BodyMarkdown: "引用已移除"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, item.ID); !errors.Is(err, media.ErrInUse) {
		t.Fatalf("published reference was not protected: %v", err)
	}
	article, err = publishingService.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Join(root, filepath.FromSlash(item.ObjectKey)))); !os.IsNotExist(err) {
		t.Fatalf("media directory still exists: %v", err)
	}
}
