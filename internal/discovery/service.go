package discovery

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

var ErrInvalidQuery = errors.New("search query is invalid")

type Options struct {
	BaseURL       string
	SyncBatchSize int
	MaxResults    int
	FeedLimit     int
	SitemapLimit  int
}

func DefaultOptions() Options {
	return Options{
		BaseURL:       "http://localhost:8080",
		SyncBatchSize: 20,
		MaxResults:    50,
		FeedLimit:     50,
		SitemapLimit:  50_000,
	}
}

type Service struct {
	repository *Repository
	options    Options
	baseURL    string
}

func NewService(repository *Repository, configured ...Options) (*Service, error) {
	options := DefaultOptions()
	if len(configured) > 0 {
		options = configured[0]
		defaults := DefaultOptions()
		if options.SyncBatchSize < 1 {
			options.SyncBatchSize = defaults.SyncBatchSize
		}
		if options.MaxResults < 1 {
			options.MaxResults = defaults.MaxResults
		}
		if options.FeedLimit < 1 {
			options.FeedLimit = defaults.FeedLimit
		}
		if options.SitemapLimit < 1 {
			options.SitemapLimit = defaults.SitemapLimit
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(options.BaseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("discovery base URL must be an absolute HTTP or HTTPS URL without credentials, query, or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	baseURL := strings.TrimRight(parsed.String(), "/")
	return &Service{repository: repository, options: options, baseURL: baseURL}, nil
}

func (s *Service) BaseURL() string { return s.baseURL }

func (s *Service) AbsoluteURL(path string) string {
	if path == "/" {
		return s.baseURL + "/"
	}
	return s.baseURL + (&url.URL{Path: path}).EscapedPath()
}

func (s *Service) SyncDirty(ctx context.Context) (int, error) {
	return s.repository.SyncDirty(ctx, s.options.SyncBatchSize)
}

func (s *Service) SyncAllDirty(ctx context.Context) (int, error) {
	total := 0
	for {
		count, err := s.SyncDirty(ctx)
		total += count
		if err != nil || count < s.options.SyncBatchSize {
			return total, err
		}
	}
}

func (s *Service) Search(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	page, err := s.SearchPage(ctx, query)
	if err != nil {
		return nil, err
	}
	return page.Results, nil
}

func (s *Service) SearchPage(ctx context.Context, query SearchQuery) (SearchPage, error) {
	query.Text = strings.TrimSpace(query.Text)
	if len([]rune(query.Text)) < 2 || len([]rune(query.Text)) > 100 {
		return SearchPage{}, ErrInvalidQuery
	}
	if query.Kind != "" && query.Kind != "article" && query.Kind != "page" {
		return SearchPage{}, ErrInvalidQuery
	}
	if query.Sort != "" && query.Sort != "relevance" && query.Sort != "newest" {
		return SearchPage{}, ErrInvalidQuery
	}
	perPage := query.PerPage
	if perPage < 1 {
		perPage = query.Limit
	}
	request := pagination.Normalize(pagination.Request{Page: query.Page, PerPage: perPage}, s.options.MaxResults, s.options.MaxResults)
	query.Page, query.PerPage, query.Limit = request.Page, request.PerPage, request.PerPage
	if _, err := s.SyncAllDirty(ctx); err != nil {
		return SearchPage{}, err
	}
	return s.repository.SearchPage(ctx, query)
}

func (s *Service) Feed(ctx context.Context) ([]FeedItem, error) {
	return s.repository.Feed(ctx, s.options.FeedLimit)
}

func (s *Service) Sitemap(ctx context.Context) ([]SitemapEntry, error) {
	return s.repository.Sitemap(ctx, s.options.SitemapLimit)
}

func (s *Service) ArchiveIndex(ctx context.Context) ([]ArchiveYear, error) {
	return s.repository.ArchiveIndex(ctx)
}

func (s *Service) ArchiveMonthPage(ctx context.Context, year, month int, request pagination.Request) (ArchivePage, error) {
	if year < 1970 || year > 9999 || month < 1 || month > 12 {
		return ArchivePage{}, ErrNotFound
	}
	request = pagination.Normalize(request, 20, 50)
	return s.repository.ArchiveMonthPage(ctx, year, month, request)
}

func (s *Service) ResolveRedirect(ctx context.Context, pathKey string) (Redirect, error) {
	return s.repository.ResolveRedirect(ctx, pathKey)
}
