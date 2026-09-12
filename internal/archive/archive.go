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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/organization"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	"github.com/zhushilin/blog-project/internal/publishing"
)

const (
	Format        = "personal-blog-content-archive"
	FormatVersion = 2
	legacyVersion = 1
	maxEntries    = 100000
	maxFileSize   = 32 << 20
	// Bound the total decompressed payload as well as each individual body so
	// verification cannot be used to expand a highly compressed ZIP without
	// limit.
	maxUnpackedSize = 256 << 20
)

type Manifest struct {
	Format             string                `json:"format"`
	Version            int                   `json:"version"`
	CreatedAt          time.Time             `json:"created_at"`
	ApplicationVersion string                `json:"application_version,omitempty"`
	MigrationVersion   int                   `json:"migration_version,omitempty"`
	Site               SiteManifest          `json:"site,omitempty"`
	Redirects          []RedirectEntry       `json:"redirects,omitempty"`
	MediaReferences    []MediaReferenceEntry `json:"media_references,omitempty"`
	Entries            []Entry               `json:"entries"`
}

// SiteManifest deliberately contains public presentation settings only. It
// has no owner, authentication, plugin-secret, or delivery-provider fields.
type SiteManifest struct {
	Name                  string   `json:"name,omitempty"`
	PrimaryLanguage       string   `json:"primary_language,omitempty"`
	Timezone              string   `json:"timezone,omitempty"`
	BaseURL               string   `json:"base_url,omitempty"`
	Description           string   `json:"description,omitempty"`
	DefaultSEOTitle       string   `json:"default_seo_title,omitempty"`
	DefaultSEODescription string   `json:"default_seo_description,omitempty"`
	FeedSummaryMode       string   `json:"feed_summary_mode,omitempty"`
	SocialLinks           []string `json:"social_links,omitempty"`
	DefaultSocialImageID  string   `json:"default_social_image_id,omitempty"`
}

type RedirectEntry struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	StatusCode int    `json:"status_code"`
	Reason     string `json:"reason,omitempty"`
}

type MediaReferenceEntry struct {
	ContentPublicID string `json:"content_public_id"`
	MediaPublicID   string `json:"media_public_id"`
	Relation        string `json:"relation"`
}

type Entry struct {
	PublicID           string          `json:"public_id"`
	Kind               string          `json:"kind"`
	Title              string          `json:"title"`
	Slug               string          `json:"slug"`
	Excerpt            string          `json:"excerpt"`
	SEOTitle           string          `json:"seo_title"`
	SEODescription     string          `json:"seo_description"`
	CategoryPublicID   string          `json:"category_public_id,omitempty"`
	TagPublicIDsJSON   string          `json:"tag_public_ids_json,omitempty"`
	CoverMediaPublicID string          `json:"cover_media_public_id,omitempty"`
	BodyPath           string          `json:"body_path"`
	BodySize           int64           `json:"body_size"`
	BodySHA256         string          `json:"body_sha256"`
	Revisions          []RevisionEntry `json:"revisions,omitempty"`
}

type RevisionEntry struct {
	PublicID           string `json:"public_id"`
	Number             int64  `json:"number"`
	Title              string `json:"title"`
	Slug               string `json:"slug"`
	Excerpt            string `json:"excerpt"`
	SEOTitle           string `json:"seo_title"`
	SEODescription     string `json:"seo_description"`
	CategoryPublicID   string `json:"category_public_id,omitempty"`
	TagPublicIDsJSON   string `json:"tag_public_ids_json,omitempty"`
	CoverMediaPublicID string `json:"cover_media_public_id,omitempty"`
	BodyPath           string `json:"body_path"`
	BodySize           int64  `json:"body_size"`
	BodySHA256         string `json:"body_sha256"`
}

type ExportOptions struct {
	ApplicationVersion string
	MigrationVersion   int
	Site               SiteManifest
	Redirects          []RedirectEntry
}

type ImportOptions struct {
	// ApplySite is intentionally an explicit callback. Archive verification
	// never changes site settings; callers must opt in after validation and
	// should map only the public SiteManifest fields into their own service.
	ApplySite func(context.Context, SiteManifest) error
	DryRun    bool
}

type archiveBody struct {
	Path string
	Body string
}

var mediaURLPattern = regexp.MustCompile(`/media/([0-9a-fA-F]{32})/(?:original|w[0-9]+)/`)

type Verified struct {
	Manifest  Manifest
	SizeBytes int64
}

func Export(ctx context.Context, service *publishing.Service, output string) (Manifest, error) {
	return ExportWithOptions(ctx, service, ExportOptions{}, output)
}

func ExportWithOptions(ctx context.Context, service *publishing.Service, options ExportOptions, output string) (Manifest, error) {
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
	redirects := append([]RedirectEntry(nil), options.Redirects...)
	if len(redirects) == 0 {
		if provider, ok := any(service).(interface {
			Redirects(context.Context, int) ([]organization.Redirect, error)
		}); ok {
			values, err := provider.Redirects(ctx, maxEntries)
			if err != nil {
				return Manifest{}, err
			}
			redirects = make([]RedirectEntry, 0, len(values))
			for _, value := range values {
				redirects = append(redirects, RedirectEntry{SourcePath: value.SourcePath, TargetPath: value.TargetPath, StatusCode: value.StatusCode, Reason: value.Reason})
			}
		}
	}
	manifest := Manifest{Format: Format, Version: FormatVersion, CreatedAt: time.Now().UTC(), ApplicationVersion: options.ApplicationVersion, MigrationVersion: options.MigrationVersion, Site: options.Site, Redirects: redirects, Entries: make([]Entry, 0, len(items))}
	bodyFiles := make([]archiveBody, 0, len(items))
	mediaReferences := make(map[string]MediaReferenceEntry)
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
		manifestEntry := Entry{PublicID: publicID, Kind: item.Kind, Title: item.Title, Slug: item.Slug, Excerpt: item.Excerpt, SEOTitle: item.SEOTitle, SEODescription: item.SEODescription, CoverMediaPublicID: coverPublicID, BodyPath: bodyPath, BodySize: int64(len(item.BodyMarkdown)), BodySHA256: hex.EncodeToString(hash[:])}
		if item.Category != nil {
			manifestEntry.CategoryPublicID, err = platformid.EncodePublicID(item.Category.PublicID)
			if err != nil {
				return Manifest{}, err
			}
		}
		manifestEntry.TagPublicIDsJSON, err = encodeTagPublicIDs(item.Tags)
		if err != nil {
			return Manifest{}, err
		}
		bodyFiles = append(bodyFiles, archiveBody{Path: bodyPath, Body: item.BodyMarkdown})
		addMediaReferences(mediaReferences, publicID, item.BodyMarkdown, "body")
		if coverPublicID != "" {
			addMediaReference(mediaReferences, publicID, coverPublicID, "cover")
		}
		revisions, revisionsErr := service.RevisionsWithBodies(ctx, item.Kind, item.ID)
		if revisionsErr != nil && !errors.Is(revisionsErr, publishing.ErrNotFound) {
			return Manifest{}, revisionsErr
		}
		sort.Slice(revisions, func(left, right int) bool { return revisions[left].Number < revisions[right].Number })
		for _, revision := range revisions {
			revisionPublicID, err := platformid.EncodePublicID(revision.PublicID)
			if err != nil {
				return Manifest{}, err
			}
			revisionPath := "revisions/" + revisionPublicID + ".md"
			revisionHash := sha256.Sum256([]byte(revision.BodyMarkdown))
			revisionCoverID := ""
			if len(revision.CoverMediaPublicID) > 0 {
				revisionCoverID, err = platformid.EncodePublicID(revision.CoverMediaPublicID)
				if err != nil {
					return Manifest{}, err
				}
			}
			revisionEntry := RevisionEntry{PublicID: revisionPublicID, Number: revision.Number, Title: revision.Title, Slug: revision.Slug, Excerpt: revision.Excerpt, SEOTitle: revision.SEOTitle, SEODescription: revision.SEODescription, CoverMediaPublicID: revisionCoverID, BodyPath: revisionPath, BodySize: int64(len(revision.BodyMarkdown)), BodySHA256: hex.EncodeToString(revisionHash[:])}
			if len(revision.CategoryPublicID) > 0 {
				revisionEntry.CategoryPublicID, err = platformid.EncodePublicID(revision.CategoryPublicID)
				if err != nil {
					return Manifest{}, err
				}
			}
			revisionEntry.TagPublicIDsJSON = revision.TagPublicIDsJSON
			manifestEntry.Revisions = append(manifestEntry.Revisions, revisionEntry)
			bodyFiles = append(bodyFiles, archiveBody{Path: revisionPath, Body: revision.BodyMarkdown})
			addMediaReferences(mediaReferences, publicID, revision.BodyMarkdown, "body")
			if revisionCoverID != "" {
				addMediaReference(mediaReferences, publicID, revisionCoverID, "cover")
			}
		}
		manifest.Entries = append(manifest.Entries, manifestEntry)
	}
	if len(bodyFiles)+1 > maxEntries {
		return Manifest{}, errors.New("archive has too many body files")
	}
	var bodyBytes int64
	for _, bodyFile := range bodyFiles {
		size := int64(len(bodyFile.Body))
		if size > maxFileSize || bodyBytes > maxUnpackedSize-size {
			return Manifest{}, errors.New("archive body payload exceeds limit")
		}
		bodyBytes += size
	}
	for _, reference := range mediaReferences {
		manifest.MediaReferences = append(manifest.MediaReferences, reference)
	}
	sort.Slice(manifest.MediaReferences, func(left, right int) bool {
		if manifest.MediaReferences[left].ContentPublicID == manifest.MediaReferences[right].ContentPublicID {
			return manifest.MediaReferences[left].MediaPublicID < manifest.MediaReferences[right].MediaPublicID
		}
		return manifest.MediaReferences[left].ContentPublicID < manifest.MediaReferences[right].ContentPublicID
	})
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
	for _, item := range bodyFiles {
		if err := ctx.Err(); err != nil {
			writer.Close()
			return Manifest{}, err
		}
		header := &zip.FileHeader{Name: item.Path, Method: zip.Deflate}
		header.SetMode(0o600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			writer.Close()
			return Manifest{}, err
		}
		if _, err := io.WriteString(entry, item.Body); err != nil {
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
		if !safePath(item.Name) || (!strings.HasPrefix(item.Name, "content/") && !strings.HasPrefix(item.Name, "revisions/")) || item.UncompressedSize64 > maxFileSize {
			return Verified{}, fmt.Errorf("unsafe archive entry %q", item.Name)
		}
		if seen[item.Name] {
			return Verified{}, fmt.Errorf("duplicate archive entry %q", item.Name)
		}
		seen[item.Name] = true
	}
	if manifest.Format != Format || (manifest.Version != legacyVersion && manifest.Version != FormatVersion) || manifest.CreatedAt.IsZero() {
		return Verified{}, errors.New("archive manifest metadata is invalid")
	}
	if len(manifest.Entries) > maxEntries {
		return Verified{}, errors.New("archive manifest has too many entries")
	}
	if err := validateManifestMetadata(manifest); err != nil {
		return Verified{}, err
	}
	expectedFiles := map[string]struct{}{"manifest.json": {}}
	seenPublicIDs := make(map[string]struct{}, len(manifest.Entries))
	seenRevisionIDs := make(map[string]struct{})
	for _, expected := range manifest.Entries {
		if _, exists := seenPublicIDs[expected.PublicID]; exists {
			return Verified{}, fmt.Errorf("duplicate content public ID %q", expected.PublicID)
		}
		seenPublicIDs[expected.PublicID] = struct{}{}
		expectedFiles[expected.BodyPath] = struct{}{}
		for _, revision := range expected.Revisions {
			if _, exists := seenRevisionIDs[revision.PublicID]; exists {
				return Verified{}, fmt.Errorf("duplicate revision public ID %q", revision.PublicID)
			}
			seenRevisionIDs[revision.PublicID] = struct{}{}
			expectedFiles[revision.BodyPath] = struct{}{}
		}
	}
	if len(seen) != len(expectedFiles) {
		return Verified{}, errors.New("archive contains files not listed in manifest")
	}
	for name := range seen {
		if _, ok := expectedFiles[name]; !ok {
			return Verified{}, fmt.Errorf("archive file %q is not listed in manifest", name)
		}
	}
	for _, expected := range manifest.Entries {
		if err := validateContentEntry(expected, manifest.Version); err != nil {
			return Verified{}, fmt.Errorf("invalid archive manifest entry")
		}
		if err := verifyBody(reader.File, expected.BodyPath, expected.BodySize, expected.BodySHA256); err != nil {
			return Verified{}, err
		}
		for _, revision := range expected.Revisions {
			if err := validateRevisionEntry(revision); err != nil {
				return Verified{}, fmt.Errorf("invalid archive revision entry")
			}
			if err := verifyBody(reader.File, revision.BodyPath, revision.BodySize, revision.BodySHA256); err != nil {
				return Verified{}, err
			}
		}
	}
	return Verified{Manifest: manifest, SizeBytes: info.Size()}, nil
}

func Import(ctx context.Context, service *publishing.Service, archivePath string) (int, error) {
	report, err := ImportWithOptions(ctx, service, archivePath, ImportOptions{})
	return report.Created, err
}

type ImportReport struct {
	Created   int
	Planned   int
	Conflicts int
	Warnings  []string
}

func ImportWithReport(ctx context.Context, service *publishing.Service, archivePath string) (ImportReport, error) {
	return ImportWithOptions(ctx, service, archivePath, ImportOptions{})
}

func ImportWithOptions(ctx context.Context, service *publishing.Service, archivePath string, options ImportOptions) (ImportReport, error) {
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
	checkedMedia := make(map[string]struct{})
	type createdDraft struct {
		Kind     string
		PublicID []byte
	}
	createdDrafts := make([]createdDraft, 0, len(verified.Manifest.Entries))
	rollbackDrafts := func() error {
		var rollbackErr error
		for index := len(createdDrafts) - 1; index >= 0; index-- {
			if err := service.DeleteImportedDraft(ctx, createdDrafts[index].Kind, createdDrafts[index].PublicID); err != nil {
				rollbackErr = errors.Join(rollbackErr, err)
			}
		}
		return rollbackErr
	}

	redirectProvider, hasRedirectProvider := any(service).(interface {
		RedirectBySource(context.Context, string) (organization.Redirect, error)
		CreateRedirect(context.Context, organization.RedirectInput) error
		DeleteRedirect(context.Context, int64) error
	})
	redirectsToCreate := append([]RedirectEntry(nil), verified.Manifest.Redirects...)
	if !options.DryRun && hasRedirectProvider {
		redirectsToCreate = redirectsToCreate[:0]
		for _, redirect := range verified.Manifest.Redirects {
			existing, lookupErr := redirectProvider.RedirectBySource(ctx, redirect.SourcePath)
			if lookupErr == nil {
				if existing.TargetPath == redirect.TargetPath && existing.StatusCode == redirect.StatusCode {
					continue
				}
				result.Conflicts++
				result.Warnings = append(result.Warnings, fmt.Sprintf("redirect %q conflicts with an existing redirect", redirect.SourcePath))
				return result, nil
			}
			if !errors.Is(lookupErr, organization.ErrNotFound) {
				return result, lookupErr
			}
			redirectsToCreate = append(redirectsToCreate, redirect)
		}
	}
	// Check stable identities before creating any draft. Besides making dry-run
	// useful for repeated imports, this keeps a mixed archive from creating a
	// prefix of entries before a later content or revision ID conflict is found.
	for _, entry := range verified.Manifest.Entries {
		contentPublicID, decodeErr := platformid.DecodePublicID(entry.PublicID)
		if decodeErr != nil {
			return result, decodeErr
		}
		contentExists, existsErr := service.ContentPublicIDExists(ctx, entry.Kind, contentPublicID)
		if existsErr != nil {
			return result, existsErr
		}
		if contentExists {
			result.Conflicts++
			result.Warnings = append(result.Warnings, fmt.Sprintf("entry %q conflicts with an existing content public ID", entry.Slug))
		}
		for _, revision := range entry.Revisions {
			revisionPublicID, revisionErr := platformid.DecodePublicID(revision.PublicID)
			if revisionErr != nil {
				return result, revisionErr
			}
			revisionExists, existsErr := service.RevisionPublicIDExists(ctx, revisionPublicID)
			if existsErr != nil {
				return result, existsErr
			}
			if revisionExists {
				result.Conflicts++
				result.Warnings = append(result.Warnings, fmt.Sprintf("entry %q conflicts with an existing revision public ID", entry.Slug))
			}
		}
	}
	if result.Conflicts > 0 {
		return result, nil
	}
	for _, entry := range verified.Manifest.Entries {
		if err := ctx.Err(); err != nil {
			rollbackErr := rollbackDrafts()
			return result, errors.Join(err, rollbackErr)
		}
		revisions := append([]RevisionEntry(nil), entry.Revisions...)
		if len(revisions) == 0 {
			revisions = []RevisionEntry{{Title: entry.Title, Slug: entry.Slug, Excerpt: entry.Excerpt, SEOTitle: entry.SEOTitle, SEODescription: entry.SEODescription, CategoryPublicID: entry.CategoryPublicID, TagPublicIDsJSON: entry.TagPublicIDsJSON, CoverMediaPublicID: entry.CoverMediaPublicID, BodyPath: entry.BodyPath}}
		}
		sort.SliceStable(revisions, func(left, right int) bool { return revisions[left].Number < revisions[right].Number })
		inputs := make([]publishing.DraftInput, 0, len(revisions))
		entryError := ""
		contentPublicID, contentIDErr := platformid.DecodePublicID(entry.PublicID)
		if contentIDErr != nil {
			entryError = contentIDErr.Error()
		}
		for _, revision := range revisions {
			if entryError != "" {
				break
			}
			coverMediaPublicID, coverErr := decodeAndCheckCover(ctx, service, revision.CoverMediaPublicID)
			if coverErr != nil {
				entryError = coverErr.Error()
				break
			}
			body, bodyErr := readBody(reader.File, revision.BodyPath)
			if bodyErr != nil {
				rollbackErr := rollbackDrafts()
				return result, errors.Join(bodyErr, rollbackErr)
			}
			if mediaErr := decodeAndCheckBodyMedia(ctx, service, string(body), checkedMedia); mediaErr != nil {
				entryError = mediaErr.Error()
				break
			}
			var revisionPublicID []byte
			if revision.PublicID != "" {
				revisionPublicID, bodyErr = platformid.DecodePublicID(revision.PublicID)
				if bodyErr != nil {
					entryError = bodyErr.Error()
					break
				}
			}
			inputData := publishing.DraftInput{ContentPublicID: contentPublicID, RevisionPublicID: revisionPublicID, RevisionNumber: revision.Number, Title: revision.Title, Slug: revision.Slug, Excerpt: revision.Excerpt, SEOTitle: revision.SEOTitle, SEODescription: revision.SEODescription, BodyMarkdown: string(body), CoverMediaPublicID: coverMediaPublicID}
			if taxonomy, taxonomyErr := resolveTaxonomy(ctx, service, revision.CategoryPublicID, revision.TagPublicIDsJSON); taxonomyErr != nil {
				entryError = fmt.Sprintf("taxonomy could not be resolved: %v", taxonomyErr)
				break
			} else {
				inputData.CategoryID, inputData.TagIDs = taxonomy.CategoryID, taxonomy.TagIDs
			}
			if inputData.Slug == "" {
				inputData.Slug = entry.Slug
			}
			inputs = append(inputs, inputData)
		}
		if entryError != "" {
			result.Conflicts++
			result.Warnings = append(result.Warnings, fmt.Sprintf("entry %q skipped: %s", entry.Slug, entryError))
			continue
		}
		if options.DryRun {
			result.Planned++
			continue
		}
		_, err = service.ImportDraftRevisions(ctx, entry.Kind, inputs)
		if err != nil {
			result.Conflicts++
			result.Warnings = append(result.Warnings, fmt.Sprintf("entry %q was not imported: %v", entry.Slug, err))
			rollbackErr := rollbackDrafts()
			result.Created = 0
			if rollbackErr != nil {
				return result, fmt.Errorf("archive import rollback failed: %w", rollbackErr)
			}
			return result, nil
		}
		result.Created++
		createdDrafts = append(createdDrafts, createdDraft{Kind: entry.Kind, PublicID: append([]byte(nil), contentPublicID...)})
	}
	if !options.DryRun && result.Conflicts > 0 {
		if rollbackErr := rollbackDrafts(); rollbackErr != nil {
			return result, fmt.Errorf("archive import rollback failed: %w", rollbackErr)
		}
		result.Created = 0
		return result, nil
	}
	createdRedirectIDs := make([]int64, 0, len(redirectsToCreate))
	rollbackRedirects := func() {
		if !hasRedirectProvider {
			return
		}
		for index := len(createdRedirectIDs) - 1; index >= 0; index-- {
			_ = redirectProvider.DeleteRedirect(ctx, createdRedirectIDs[index])
		}
	}
	if !options.DryRun {
		if hasRedirectProvider {
			for _, redirect := range redirectsToCreate {
				if err := redirectProvider.CreateRedirect(ctx, organization.RedirectInput{SourcePath: redirect.SourcePath, TargetPath: redirect.TargetPath, StatusCode: redirect.StatusCode}); err != nil {
					result.Conflicts++
					result.Warnings = append(result.Warnings, fmt.Sprintf("redirect %q was not imported: %v", redirect.SourcePath, err))
					rollbackRedirects()
					if rollbackErr := rollbackDrafts(); rollbackErr != nil {
						return result, fmt.Errorf("archive import rollback failed: %w", rollbackErr)
					}
					result.Created = 0
					return result, nil
				}
				if created, lookupErr := redirectProvider.RedirectBySource(ctx, redirect.SourcePath); lookupErr == nil {
					createdRedirectIDs = append(createdRedirectIDs, created.ID)
				}
			}
		}
		if result.Conflicts == 0 && options.ApplySite != nil && hasSiteManifest(verified.Manifest.Site) {
			if err := options.ApplySite(ctx, verified.Manifest.Site); err != nil {
				rollbackRedirects()
				_ = rollbackDrafts()
				return result, fmt.Errorf("apply archived site settings: %w", err)
			}
		}
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

func addMediaReferences(references map[string]MediaReferenceEntry, contentPublicID, body, relation string) {
	for _, match := range mediaURLPattern.FindAllStringSubmatch(body, -1) {
		addMediaReference(references, contentPublicID, strings.ToLower(match[1]), relation)
	}
}

func addMediaReference(references map[string]MediaReferenceEntry, contentPublicID, mediaPublicID, relation string) {
	key := contentPublicID + "\x00" + mediaPublicID + "\x00" + relation
	references[key] = MediaReferenceEntry{ContentPublicID: contentPublicID, MediaPublicID: mediaPublicID, Relation: relation}
}

func encodeTagPublicIDs(tags []organization.Tag) (string, error) {
	if len(tags) == 0 {
		return "", nil
	}
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		value, err := platformid.EncodePublicID(tag.PublicID)
		if err != nil {
			return "", err
		}
		values = append(values, value)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func validateManifestMetadata(manifest Manifest) error {
	if len(manifest.ApplicationVersion) > 200 || manifest.MigrationVersion < 0 {
		return errors.New("archive manifest version metadata is invalid")
	}
	if err := validateSiteManifest(manifest.Site); err != nil {
		return err
	}
	if len(manifest.Redirects) > maxEntries {
		return errors.New("archive contains too many redirects")
	}
	seenRedirects := make(map[string]struct{}, len(manifest.Redirects))
	for _, redirect := range manifest.Redirects {
		if !validRedirectPath(redirect.SourcePath) || !validRedirectPath(redirect.TargetPath) || redirect.SourcePath == redirect.TargetPath || (redirect.StatusCode != 301 && redirect.StatusCode != 308) || len(redirect.Reason) > 200 || strings.ContainsAny(redirect.Reason, "\x00\r\n") {
			return errors.New("archive redirect metadata is invalid")
		}
		if _, exists := seenRedirects[redirect.SourcePath]; exists {
			return fmt.Errorf("duplicate archive redirect %q", redirect.SourcePath)
		}
		seenRedirects[redirect.SourcePath] = struct{}{}
	}
	if len(manifest.MediaReferences) > maxEntries*4 {
		return errors.New("archive contains too many media references")
	}
	contentIDs := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		contentIDs[entry.PublicID] = struct{}{}
	}
	seenReferences := make(map[string]struct{}, len(manifest.MediaReferences))
	for _, reference := range manifest.MediaReferences {
		if !validPublicID(reference.ContentPublicID) || !validPublicID(reference.MediaPublicID) || (reference.Relation != "body" && reference.Relation != "cover") {
			return errors.New("archive media reference metadata is invalid")
		}
		if _, exists := contentIDs[reference.ContentPublicID]; !exists {
			return errors.New("archive media reference points to unknown content")
		}
		key := reference.ContentPublicID + "\x00" + reference.MediaPublicID + "\x00" + reference.Relation
		if _, exists := seenReferences[key]; exists {
			return errors.New("duplicate archive media reference")
		}
		seenReferences[key] = struct{}{}
	}
	return nil
}

func validateSiteManifest(site SiteManifest) error {
	if len(site.Name) > 300 || len(site.PrimaryLanguage) > 32 || len(site.Timezone) > 100 || len(site.Description) > 2000 || len(site.DefaultSEOTitle) > 300 || len(site.DefaultSEODescription) > 2000 || len(site.SocialLinks) > 10 {
		return errors.New("archive site metadata is too large")
	}
	if site.BaseURL != "" && !validHTTPURL(site.BaseURL) {
		return errors.New("archive site base URL is invalid")
	}
	if site.Timezone != "" {
		if _, err := time.LoadLocation(site.Timezone); err != nil {
			return errors.New("archive site timezone is invalid")
		}
	}
	for _, link := range site.SocialLinks {
		if !validHTTPURL(link) {
			return errors.New("archive social link is invalid")
		}
	}
	if site.DefaultSocialImageID != "" && !validPublicID(site.DefaultSocialImageID) {
		return errors.New("archive default social image ID is invalid")
	}
	if site.FeedSummaryMode != "" && site.FeedSummaryMode != "excerpt" && site.FeedSummaryMode != "full" {
		return errors.New("archive feed summary mode is invalid")
	}
	return nil
}

func validHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validRedirectPath(value string) bool {
	if value == "" || !strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsAny(value, "\x00\r\n") || path.Clean(value) != value || len(value) > 512 {
		return false
	}
	if value == "/admin" || strings.HasPrefix(value, "/admin/") || value == "/assets" || strings.HasPrefix(value, "/assets/") {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Path == value && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Scheme == "" && parsed.Host == ""
}

func validateContentEntry(entry Entry, version int) error {
	if !validPublicID(entry.PublicID) || (entry.Kind != "article" && entry.Kind != "page") || entry.CoverMediaPublicID != "" && !validPublicID(entry.CoverMediaPublicID) || !validTaxonomy(entry.CategoryPublicID, entry.TagPublicIDsJSON) || entry.BodyPath != "content/"+entry.PublicID+".md" || entry.BodySize < 0 || entry.BodySize > maxFileSize || !validSHA256(entry.BodySHA256) {
		return errors.New("invalid content entry")
	}
	if version == legacyVersion && len(entry.Revisions) > 0 {
		return errors.New("legacy archive cannot contain revisions")
	}
	seenNumbers := make(map[int64]struct{}, len(entry.Revisions))
	for _, revision := range entry.Revisions {
		if err := validateRevisionEntry(revision); err != nil {
			return err
		}
		if _, exists := seenNumbers[revision.Number]; exists {
			return errors.New("duplicate revision number")
		}
		seenNumbers[revision.Number] = struct{}{}
	}
	return nil
}

func validateRevisionEntry(entry RevisionEntry) error {
	if !validPublicID(entry.PublicID) || entry.Number < 1 || entry.CoverMediaPublicID != "" && !validPublicID(entry.CoverMediaPublicID) || !validTaxonomy(entry.CategoryPublicID, entry.TagPublicIDsJSON) || entry.BodyPath != "revisions/"+entry.PublicID+".md" || entry.BodySize < 0 || entry.BodySize > maxFileSize || !validSHA256(entry.BodySHA256) {
		return errors.New("invalid revision entry")
	}
	return nil
}

func validTaxonomy(categoryPublicID, tagPublicIDsJSON string) bool {
	if categoryPublicID != "" && !validPublicID(categoryPublicID) {
		return false
	}
	if tagPublicIDsJSON == "" {
		return true
	}
	var values []string
	if json.Unmarshal([]byte(tagPublicIDsJSON), &values) != nil || len(values) > 30 {
		return false
	}
	for _, value := range values {
		if !validPublicID(value) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func verifyBody(files []*zip.File, name string, expectedSize int64, expectedHash string) error {
	file, ok := findZip(files, name)
	if !ok {
		return fmt.Errorf("missing body %q", name)
	}
	input, err := file.Open()
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, io.LimitReader(input, maxFileSize+1))
	closeErr := input.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if size != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return fmt.Errorf("body checksum mismatch for %q", name)
	}
	return nil
}

func readBody(files []*zip.File, name string) ([]byte, error) {
	file, ok := findZip(files, name)
	if !ok {
		return nil, fmt.Errorf("missing body %q", name)
	}
	input, err := file.Open()
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(input, maxFileSize+1))
	closeErr := input.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return body, nil
}

func decodeAndCheckCover(ctx context.Context, service *publishing.Service, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := platformid.DecodePublicID(value)
	if err != nil {
		return nil, err
	}
	if _, err := service.ResolveMediaPublicID(ctx, decoded); err != nil {
		return nil, fmt.Errorf("cover media %s is not available", value)
	}
	return decoded, nil
}

func decodeAndCheckBodyMedia(ctx context.Context, service *publishing.Service, body string, checked map[string]struct{}) error {
	for _, match := range mediaURLPattern.FindAllStringSubmatch(body, -1) {
		value := strings.ToLower(match[1])
		if _, exists := checked[value]; exists {
			continue
		}
		decoded, err := platformid.DecodePublicID(value)
		if err != nil {
			return fmt.Errorf("body media %s is invalid", value)
		}
		if _, err := service.ResolveMediaPublicID(ctx, decoded); err != nil {
			return fmt.Errorf("body media %s is not available", value)
		}
		checked[value] = struct{}{}
	}
	return nil
}

type resolvedTaxonomy struct {
	CategoryID int64
	TagIDs     []int64
}

func resolveTaxonomy(ctx context.Context, service *publishing.Service, categoryPublicID, tagPublicIDsJSON string) (resolvedTaxonomy, error) {
	if categoryPublicID == "" && tagPublicIDsJSON == "" {
		return resolvedTaxonomy{}, nil
	}
	resolver, ok := any(service).(interface {
		TaxonomyBySnapshot(context.Context, []byte, string) (organization.Taxonomy, error)
	})
	if !ok {
		return resolvedTaxonomy{}, errors.New("publishing service does not expose taxonomy resolution")
	}
	var categoryID []byte
	var err error
	if categoryPublicID != "" {
		categoryID, err = platformid.DecodePublicID(categoryPublicID)
		if err != nil {
			return resolvedTaxonomy{}, err
		}
	}
	taxonomy, err := resolver.TaxonomyBySnapshot(ctx, categoryID, tagPublicIDsJSON)
	if err != nil {
		return resolvedTaxonomy{}, err
	}
	result := resolvedTaxonomy{}
	if taxonomy.Category != nil {
		result.CategoryID = taxonomy.Category.ID
	}
	for _, tag := range taxonomy.Tags {
		result.TagIDs = append(result.TagIDs, tag.ID)
	}
	return result, nil
}

func hasSiteManifest(site SiteManifest) bool {
	return site.Name != "" || site.PrimaryLanguage != "" || site.Timezone != "" || site.BaseURL != "" || site.Description != "" || site.DefaultSEOTitle != "" || site.DefaultSEODescription != "" || site.FeedSummaryMode != "" || len(site.SocialLinks) > 0 || site.DefaultSocialImageID != ""
}

func safePath(value string) bool {
	return value != "" && !strings.HasPrefix(value, "/") && !strings.Contains(value, "\\") && path.Clean(value) == value && !strings.HasPrefix(value, "../") && !strings.Contains(value, "\x00")
}

func validPublicID(value string) bool {
	_, err := platformid.DecodePublicID(value)
	return err == nil
}
