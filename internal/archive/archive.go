package archive

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	"github.com/zhushilin/blog-project/internal/publishing"
)

const (
	Format        = "personal-blog-content-archive"
	FormatVersion = 1
	maxEntries    = 100000
	maxFileSize   = 32 << 20
	// Bound the total decompressed payload as well as each individual body so
	// verification cannot be used to expand a highly compressed ZIP without
	// limit.
	maxUnpackedSize = 256 << 20
)

type Manifest struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Entries   []Entry   `json:"entries"`
}

type Entry struct {
	PublicID           string `json:"public_id"`
	Kind               string `json:"kind"`
	Title              string `json:"title"`
	Slug               string `json:"slug"`
	Excerpt            string `json:"excerpt"`
	SEOTitle           string `json:"seo_title"`
	SEODescription     string `json:"seo_description"`
	CoverMediaPublicID string `json:"cover_media_public_id,omitempty"`
	BodyPath           string `json:"body_path"`
	BodySize           int64  `json:"body_size"`
	BodySHA256         string `json:"body_sha256"`
}

type Verified struct {
	Manifest  Manifest
	SizeBytes int64
}

func Export(ctx context.Context, service *publishing.Service, output string) (Manifest, error) {
	if service == nil {
		return Manifest{}, errors.New("publishing service is required")
	}
	if strings.TrimSpace(output) == "" {
		return Manifest{}, errors.New("archive output is required")
	}
	articles, err := service.Articles(ctx)
	if err != nil {
		return Manifest{}, err
	}
	pages, err := service.Pages(ctx)
	if err != nil {
		return Manifest{}, err
	}
	items := append(articles, pages...)
	manifest := Manifest{Format: Format, Version: FormatVersion, CreatedAt: time.Now().UTC(), Entries: make([]Entry, 0, len(items))}
	for _, item := range items {
		publicID, err := platformid.EncodePublicID(item.PublicID)
		if err != nil {
			return Manifest{}, err
		}
		bodyPath := "content/" + publicID + ".md"
		hash := sha256.Sum256([]byte(item.BodyMarkdown))
		coverPublicID := ""
		if len(item.CoverMediaPublicID) > 0 {
			coverPublicID, err = platformid.EncodePublicID(item.CoverMediaPublicID)
			if err != nil {
				return Manifest{}, fmt.Errorf("encode cover media ID for %q: %w", item.Slug, err)
			}
		}
		manifest.Entries = append(manifest.Entries, Entry{PublicID: publicID, Kind: item.Kind, Title: item.Title, Slug: item.Slug, Excerpt: item.Excerpt, SEOTitle: item.SEOTitle, SEODescription: item.SEODescription, CoverMediaPublicID: coverPublicID, BodyPath: bodyPath, BodySize: int64(len(item.BodyMarkdown)), BodySHA256: hex.EncodeToString(hash[:])})
	}
	if _, err := os.Lstat(output); err == nil {
		return Manifest{}, errors.New("archive output already exists")
	} else if !os.IsNotExist(err) {
		return Manifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return Manifest{}, err
	}
	file, err := os.OpenFile(output+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Manifest{}, err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(output + ".partial")
		}
	}()
	writer := zip.NewWriter(file)
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			writer.Close()
			return Manifest{}, err
		}
		publicID, _ := platformid.EncodePublicID(item.PublicID)
		header := &zip.FileHeader{Name: "content/" + publicID + ".md", Method: zip.Deflate}
		header.SetMode(0o600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			writer.Close()
			return Manifest{}, err
		}
		if _, err := io.WriteString(entry, item.BodyMarkdown); err != nil {
			writer.Close()
			return Manifest{}, err
		}
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		writer.Close()
		return Manifest{}, err
	}
	header := &zip.FileHeader{Name: "manifest.json", Method: zip.Deflate}
	header.SetMode(0o600)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		writer.Close()
		return Manifest{}, err
	}
	if _, err := entry.Write(append(manifestJSON, '\n')); err != nil {
		writer.Close()
		return Manifest{}, err
	}
	if err := writer.Close(); err != nil {
		return Manifest{}, err
	}
	if err := file.Sync(); err != nil {
		return Manifest{}, err
	}
	if err := file.Close(); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(output+".partial", output); err != nil {
		return Manifest{}, err
	}
	complete = true
	return manifest, nil
}

func Verify(ctx context.Context, archivePath string) (Verified, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return Verified{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Verified{}, err
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return Verified{}, err
	}
	if len(reader.File) < 1 || len(reader.File) > maxEntries {
		return Verified{}, errors.New("archive entry count is invalid")
	}
	var manifest Manifest
	seen := map[string]bool{}
	var unpackedSize int64
	for _, item := range reader.File {
		if err := ctx.Err(); err != nil {
			return Verified{}, err
		}
		if !item.FileInfo().Mode().IsRegular() {
			return Verified{}, fmt.Errorf("archive entry %q is not a regular file", item.Name)
		}
		if item.UncompressedSize64 > uint64(maxUnpackedSize) || unpackedSize > maxUnpackedSize-int64(item.UncompressedSize64) {
			return Verified{}, errors.New("archive unpacked size exceeds limit")
		}
		unpackedSize += int64(item.UncompressedSize64)
		if item.Name == "manifest.json" {
			if seen[item.Name] {
				return Verified{}, errors.New("manifest is duplicated")
			}
			seen[item.Name] = true
			if item.UncompressedSize64 > maxFileSize {
				return Verified{}, errors.New("manifest is too large")
			}
			input, err := item.Open()
			if err != nil {
				return Verified{}, err
			}
			contents, err := io.ReadAll(io.LimitReader(input, maxFileSize+1))
			input.Close()
			if err != nil {
				return Verified{}, err
			}
			if err := json.Unmarshal(contents, &manifest); err != nil {
				return Verified{}, err
			}
			continue
		}
		if !safePath(item.Name) || !strings.HasPrefix(item.Name, "content/") || item.UncompressedSize64 > maxFileSize {
			return Verified{}, fmt.Errorf("unsafe archive entry %q", item.Name)
		}
		if seen[item.Name] {
			return Verified{}, fmt.Errorf("duplicate archive entry %q", item.Name)
		}
		seen[item.Name] = true
	}
	if manifest.Format != Format || manifest.Version != FormatVersion || manifest.CreatedAt.IsZero() {
		return Verified{}, errors.New("archive manifest metadata is invalid")
	}
	if len(manifest.Entries) > maxEntries {
		return Verified{}, errors.New("archive manifest has too many entries")
	}
	if len(seen) != len(manifest.Entries)+1 {
		return Verified{}, errors.New("archive contains files not listed in manifest")
	}
	for _, expected := range manifest.Entries {
		if _, err := platformid.DecodePublicID(expected.PublicID); err != nil || expected.Kind != "article" && expected.Kind != "page" || expected.CoverMediaPublicID != "" && !validPublicID(expected.CoverMediaPublicID) || !safePath(expected.BodyPath) || expected.BodySize < 0 || expected.BodySize > maxFileSize || len(expected.BodySHA256) != sha256.Size*2 {
			return Verified{}, fmt.Errorf("invalid archive manifest entry")
		}
		file, ok := findZip(reader.File, expected.BodyPath)
		if !ok {
			return Verified{}, fmt.Errorf("missing body %q", expected.BodyPath)
		}
		input, err := file.Open()
		if err != nil {
			return Verified{}, err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, io.LimitReader(input, maxFileSize+1))
		input.Close()
		if copyErr != nil {
			return Verified{}, copyErr
		}
		if size != expected.BodySize || hex.EncodeToString(hash.Sum(nil)) != expected.BodySHA256 {
			return Verified{}, fmt.Errorf("body checksum mismatch for %q", expected.BodyPath)
		}
	}
	return Verified{Manifest: manifest, SizeBytes: info.Size()}, nil
}

func Import(ctx context.Context, service *publishing.Service, archivePath string) (int, error) {
	report, err := ImportWithReport(ctx, service, archivePath)
	return report.Created, err
}

type ImportReport struct {
	Created   int
	Conflicts int
	Warnings  []string
}

func ImportWithReport(ctx context.Context, service *publishing.Service, archivePath string) (ImportReport, error) {
	if service == nil {
		return ImportReport{}, errors.New("publishing service is required")
	}
	verified, err := Verify(ctx, archivePath)
	if err != nil {
		return ImportReport{}, err
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return ImportReport{}, err
	}
	defer file.Close()
	info, _ := file.Stat()
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return ImportReport{}, err
	}
	result := ImportReport{}
	for _, entry := range verified.Manifest.Entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var coverMediaPublicID []byte
		if entry.CoverMediaPublicID != "" {
			coverMediaPublicID, err = platformid.DecodePublicID(entry.CoverMediaPublicID)
			if err != nil {
				return result, err
			}
			if _, err := service.ResolveMediaPublicID(ctx, coverMediaPublicID); err != nil {
				result.Conflicts++
				result.Warnings = append(result.Warnings, fmt.Sprintf("entry %q skipped: cover media %s is not available", entry.Slug, entry.CoverMediaPublicID))
				continue
			}
		}
		bodyFile, _ := findZip(reader.File, entry.BodyPath)
		input, err := bodyFile.Open()
		if err != nil {
			return result, err
		}
		body, err := io.ReadAll(io.LimitReader(input, maxFileSize+1))
		input.Close()
		if err != nil {
			return result, err
		}
		inputData := publishing.DraftInput{Title: entry.Title, Slug: entry.Slug, Excerpt: entry.Excerpt, SEOTitle: entry.SEOTitle, SEODescription: entry.SEODescription, BodyMarkdown: string(body), CoverMediaPublicID: coverMediaPublicID}
		if entry.Kind == "page" {
			_, err = service.CreatePageDraft(ctx, inputData)
		} else {
			_, err = service.CreateDraft(ctx, inputData)
		}
		if err != nil {
			result.Conflicts++
			result.Warnings = append(result.Warnings, fmt.Sprintf("entry %q was not imported: %v", entry.Slug, err))
			continue
		}
		result.Created++
	}
	return result, nil
}

func findZip(files []*zip.File, name string) (*zip.File, bool) {
	for _, file := range files {
		if file.Name == name {
			return file, true
		}
	}
	return nil, false
}
func safePath(value string) bool {
	return value != "" && !strings.HasPrefix(value, "/") && !strings.Contains(value, "\\") && path.Clean(value) == value && !strings.HasPrefix(value, "../") && !strings.Contains(value, "\x00")
}

func validPublicID(value string) bool {
	_, err := platformid.DecodePublicID(value)
	return err == nil
}
