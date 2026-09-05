// Package importer provides bounded, offline migration readers.  Parsers do
// not make network requests and never publish content: every imported item is
// created as a draft, with a JSON report describing skipped and lossy fields.
package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	platformslug "github.com/zhushilin/blog-project/internal/platform/slug"
	"github.com/zhushilin/blog-project/internal/publishing"
)

const (
	FormatWordPress = "wordpress"
	FormatGhost     = "ghost"
	FormatMarkdown  = "markdown"
	maxInputBytes   = int64(256 << 20)
	maxFileBytes    = int64(32 << 20)
	maxFiles        = 100000
)

type Item struct {
	SourceID           string
	Kind               string
	Title              string
	Slug               string
	Excerpt            string
	SEOTitle           string
	SEODescription     string
	BodyMarkdown       string
	CoverMediaPublicID []byte
	Category           string
	Tags               []string
	OriginalStatus     string
	PublishedAt        *time.Time
}

type ItemReport struct {
	SourceID string `json:"source_id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	Action   string `json:"action"` // create, skip, conflict
	Reason   string `json:"reason,omitempty"`
}

type Report struct {
	Format      string       `json:"format"`
	Source      string       `json:"source"`
	Fingerprint string       `json:"fingerprint"`
	DryRun      bool         `json:"dry_run"`
	Duplicate   bool         `json:"duplicate_source"`
	Total       int          `json:"total"`
	Planned     int          `json:"planned"`
	Imported    int          `json:"imported"`
	Skipped     int          `json:"skipped"`
	Conflicts   int          `json:"conflicts"`
	Warnings    []string     `json:"warnings,omitempty"`
	Items       []ItemReport `json:"items,omitempty"`
}

type Parser interface {
	Parse([]byte, string) ([]Item, []string, error)
}

type Service struct {
	db      *database.DB
	content *publishing.Service
	org     *organization.Service
	now     func() time.Time
}

func decodeCoverMediaPublicID(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	return platformid.DecodePublicID(value)
}

func NewService(db *database.DB, content *publishing.Service, org *organization.Service) *Service {
	return &Service{db: db, content: content, org: org, now: func() time.Time { return time.Now().UTC() }}
}

func Parse(format string, data []byte, source string) ([]Item, []string, error) {
	parser, err := parserFor(format)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > maxInputBytes {
		return nil, nil, fmt.Errorf("import input exceeds %d bytes", maxInputBytes)
	}
	return parser.Parse(data, source)
}

func parserFor(format string) (Parser, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatWordPress, "wxr":
		return wordpressParser{}, nil
	case FormatGhost:
		return ghostParser{}, nil
	case FormatMarkdown, "md":
		return markdownParser{}, nil
	default:
		return nil, fmt.Errorf("unsupported import format %q", format)
	}
}

type sourceData struct {
	data []byte
	name string
	hash string
}

func readSource(format, input string) (sourceData, error) {
	if strings.TrimSpace(input) == "" {
		return sourceData{}, errors.New("import input is required")
	}
	info, err := os.Stat(input)
	if err != nil {
		return sourceData{}, err
	}
	if info.IsDir() {
		if format != FormatMarkdown && format != "md" {
			return sourceData{}, errors.New("only markdown imports accept a directory")
		}
		return readMarkdownDirectory(input)
	}
	if info.Size() > maxInputBytes {
		return sourceData{}, fmt.Errorf("import input exceeds %d bytes", maxInputBytes)
	}
	data, err := os.ReadFile(input)
	if err != nil {
		return sourceData{}, err
	}
	if format == FormatMarkdown || format == "md" {
		if strings.HasSuffix(strings.ToLower(input), ".zip") {
			data, err = readMarkdownZIP(data)
			if err != nil {
				return sourceData{}, err
			}
		}
	}
	hash := sha256.Sum256(data)
	return sourceData{data: data, name: filepath.Base(input), hash: hex.EncodeToString(hash[:])}, nil
}

func readMarkdownDirectory(root string) (sourceData, error) {
	type file struct {
		path string
		data []byte
	}
	var files []file
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("markdown entry %q is a symbolic link", path)
		}
		if len(files) >= maxFiles {
			return fmt.Errorf("markdown directory contains more than %d files", maxFiles)
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") && !strings.HasSuffix(strings.ToLower(entry.Name()), ".markdown") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("markdown entry %q is not a regular file", path)
		}
		if info.Size() > maxFileBytes || total > maxInputBytes-info.Size() {
			return fmt.Errorf("markdown input exceeds size limits")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += int64(len(data))
		rel, _ := filepath.Rel(root, path)
		files = append(files, file{path: filepath.ToSlash(rel), data: data})
		return nil
	})
	if err != nil {
		return sourceData{}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	var combined []byte
	hash := sha256.New()
	for _, value := range files {
		combined = append(combined, []byte("\n---FILE "+value.path+"---\n")...)
		combined = append(combined, value.data...)
		_, _ = hash.Write([]byte(value.path + "\x00"))
		_, _ = hash.Write(value.data)
	}
	return sourceData{data: combined, name: root, hash: hex.EncodeToString(hash.Sum(nil))}, nil
}

func readMarkdownZIP(data []byte) ([]byte, error) {
	if int64(len(data)) > maxInputBytes {
		return nil, fmt.Errorf("markdown ZIP exceeds %d bytes", maxInputBytes)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open markdown ZIP: %w", err)
	}
	type file struct {
		name string
		data []byte
	}
	var files []file
	var total int64
	for _, entry := range reader.File {
		if len(files) >= maxFiles {
			return nil, fmt.Errorf("markdown ZIP contains more than %d files", maxFiles)
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if !safeArchivePath(entry.Name) || (!strings.HasSuffix(strings.ToLower(entry.Name), ".md") && !strings.HasSuffix(strings.ToLower(entry.Name), ".markdown")) {
			continue
		}
		if entry.UncompressedSize64 > uint64(maxFileBytes) || total > maxInputBytes-int64(entry.UncompressedSize64) {
			return nil, errors.New("markdown ZIP exceeds decompressed size limits")
		}
		input, err := entry.Open()
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(input, maxFileBytes+1))
		input.Close()
		if err != nil || int64(len(body)) > maxFileBytes {
			return nil, errors.New("markdown ZIP entry is too large")
		}
		total += int64(len(body))
		files = append(files, file{name: entry.Name, data: body})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	var combined []byte
	for _, value := range files {
		combined = append(combined, []byte("\n---FILE "+value.name+"---\n")...)
		combined = append(combined, value.data...)
	}
	return combined, nil
}

func (s *Service) AnalyzePath(ctx context.Context, format, input string) (Report, error) {
	source, err := readSource(strings.ToLower(strings.TrimSpace(format)), input)
	if err != nil {
		return Report{}, err
	}
	items, warnings, err := Parse(format, source.data, source.name)
	if err != nil {
		return Report{}, err
	}
	report := Report{Format: strings.ToLower(strings.TrimSpace(format)), Source: source.name, Fingerprint: source.hash, DryRun: true, Total: len(items), Warnings: append([]string(nil), warnings...)}
	for _, item := range items {
		report.Items = append(report.Items, ItemReport{SourceID: item.SourceID, Kind: item.Kind, Title: item.Title, Slug: item.Slug, Action: "create"})
		report.Planned++
	}
	if s != nil && s.db != nil {
		var exists int
		if err := s.db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM import_runs WHERE source_format=? AND source_fingerprint=?", report.Format, report.Fingerprint).Scan(&exists); err != nil {
			return Report{}, fmt.Errorf("check previous import: %w", err)
		}
		report.Duplicate = exists > 0
		if report.Duplicate {
			report.Warnings = append(report.Warnings, "source fingerprint was already imported; execution will be skipped")
		}
	}
	return report, nil
}

func (s *Service) ImportPath(ctx context.Context, format, input string, dryRun bool) (Report, error) {
	report, err := s.AnalyzePath(ctx, format, input)
	if err != nil {
		return Report{}, err
	}
	report.DryRun = dryRun
	if dryRun || report.Duplicate {
		if report.Duplicate {
			report.Skipped = report.Total
			report.Planned = 0
			for index := range report.Items {
				report.Items[index].Action, report.Items[index].Reason = "skip", "duplicate source fingerprint"
			}
		}
		return report, nil
	}
	if s == nil || s.content == nil || s.db == nil {
		return Report{}, errors.New("import service dependencies are required")
	}
	// Re-read through the parser so this method remains safe if a caller mutates
	// an AnalyzePath report before executing it.
	source, err := readSource(strings.ToLower(strings.TrimSpace(format)), input)
	if err != nil {
		return Report{}, err
	}
	items, warnings, err := Parse(format, source.data, source.name)
	if err != nil {
		return Report{}, err
	}
	report.Warnings = append([]string(nil), warnings...)
	report.Items = report.Items[:0]
	tagCache := map[string]int64{}
	categoryCache := map[string]int64{}
	for _, item := range items {
		itemReport := ItemReport{SourceID: item.SourceID, Kind: item.Kind, Title: item.Title, Slug: item.Slug, Action: "create"}
		if item.Kind != "article" && item.Kind != "page" {
			itemReport.Action, itemReport.Reason = "skip", "unsupported content kind"
			report.Skipped++
			report.Items = append(report.Items, itemReport)
			continue
		}
		input := publishing.DraftInput{Title: item.Title, Slug: item.Slug, Excerpt: item.Excerpt, SEOTitle: item.SEOTitle, SEODescription: item.SEODescription, BodyMarkdown: item.BodyMarkdown, CoverMediaPublicID: item.CoverMediaPublicID}
		if item.Kind == "article" && s.org != nil {
			input.CategoryID = s.categoryID(ctx, item.Category, categoryCache, &report)
			for _, tag := range item.Tags {
				if id := s.tagID(ctx, tag, tagCache, &report); id > 0 {
					input.TagIDs = append(input.TagIDs, id)
				}
			}
		}
		var createErr error
		if item.Kind == "page" {
			_, createErr = s.content.CreatePageDraft(ctx, input)
		} else {
			_, createErr = s.content.CreateDraft(ctx, input)
		}
		if createErr != nil {
			itemReport.Action, itemReport.Reason = "conflict", createErr.Error()
			report.Conflicts++
		} else {
			report.Imported++
		}
		report.Items = append(report.Items, itemReport)
	}
	report.Total = len(items)
	report.Planned = report.Imported + report.Conflicts
	reportJSON, _ := json.Marshal(report)
	_, err = s.db.Writer.ExecContext(ctx, `INSERT INTO import_runs(source_format,source_fingerprint,source_name,report_json,imported_count,created_at) VALUES(?,?,?,?,?,?)`, report.Format, report.Fingerprint, report.Source, string(reportJSON), report.Imported, s.now().UnixMilli())
	if err != nil {
		return Report{}, fmt.Errorf("record import run: %w", err)
	}
	return report, nil
}

func (s *Service) categoryID(ctx context.Context, name string, cache map[string]int64, report *Report) int64 {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	key := strings.ToLower(name)
	if value := cache[key]; value > 0 {
		return value
	}
	slug := importSlug(name)
	terms, err := s.org.Categories(ctx)
	if err == nil {
		for _, term := range terms {
			if strings.EqualFold(term.Slug, slug) || strings.EqualFold(term.Name, name) {
				cache[key] = term.ID
				return term.ID
			}
		}
	}
	term, err := s.org.CreateCategory(ctx, organization.TermInput{Name: name, Slug: slug})
	if err != nil {
		report.Warnings = append(report.Warnings, "category "+name+" was not imported: "+err.Error())
		return 0
	}
	cache[key] = term.ID
	return term.ID
}

func (s *Service) tagID(ctx context.Context, name string, cache map[string]int64, report *Report) int64 {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	key := strings.ToLower(name)
	if value := cache[key]; value > 0 {
		return value
	}
	slug := importSlug(name)
	terms, err := s.org.Tags(ctx)
	if err == nil {
		for _, term := range terms {
			if strings.EqualFold(term.Slug, slug) || strings.EqualFold(term.Name, name) {
				cache[key] = term.ID
				return term.ID
			}
		}
	}
	term, err := s.org.CreateTag(ctx, organization.TermInput{Name: name, Slug: slug})
	if err != nil {
		report.Warnings = append(report.Warnings, "tag "+name+" was not imported: "+err.Error())
		return 0
	}
	cache[key] = term.ID
	return term.ID
}

func importSlug(value string) string {
	value = strings.TrimSpace(value)
	var result []rune
	hyphen := false
	for _, r := range []rune(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			result = append(result, unicode.ToLower(r))
			hyphen = false
			continue
		}
		if len(result) > 0 && !hyphen {
			result = append(result, '-')
			hyphen = true
		}
	}
	resultText := strings.Trim(string(result), "-")
	if resultText == "" {
		hash := sha256.Sum256([]byte(value))
		resultText = "imported-" + hex.EncodeToString(hash[:])[:12]
	}
	if _, key, err := platformslug.Normalize(resultText); err == nil {
		return key
	}
	hash := sha256.Sum256([]byte(value))
	return "imported-" + hex.EncodeToString(hash[:])[:12]
}

func safeArchivePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	return clean == value && clean != "." && !strings.HasPrefix(clean, "../") && clean != ".." && !strings.Contains(clean, ":")
}
