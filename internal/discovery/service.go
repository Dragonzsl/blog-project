package discovery

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

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
	repository    *Repository
	options       Options
	baseURL       string
	archiveMu     sync.RWMutex
	archiveEpoch  int64
	archive       []ArchiveYear
	searchMu      sync.Mutex
	searchItems   map[string]*list.Element
	searchRecent  *list.List
	searchBytes   int64
	searchVersion uint64
	searchFlights map[string]*searchFlight
}

const (
	searchCacheMaxEntries = 256
	searchCacheMaxBytes   = 2 << 20
	searchFlightMax       = 32
)

type searchCacheEntry struct {
	key  string
	page SearchPage
	size int64
}

// searchFlight coalesces concurrent misses for one normalized search request.
// It is deliberately bounded by searchFlightMax so a stream of distinct search
// terms cannot turn request coalescing into an unbounded memory structure.
type searchFlight struct {
	done chan struct{}
	page SearchPage
	err  error
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
	return &Service{repository: repository, options: options, baseURL: baseURL, searchItems: make(map[string]*list.Element), searchRecent: list.New(), searchFlights: make(map[string]*searchFlight)}, nil
}

func (s *Service) BaseURL() string { return s.baseURL }

// SearchCacheVersion changes whenever a dirty search projection batch is
// committed. Presentation caches can include it in their key so a completed
// background index update cannot leave a stale rendered search page at the
// same render epoch.
func (s *Service) SearchCacheVersion() uint64 {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	return s.searchVersion
}

func (s *Service) AbsoluteURL(path string) string {
	if path == "/" {
		return s.baseURL + "/"
	}
	return s.baseURL + (&url.URL{Path: path}).EscapedPath()
}

func (s *Service) SyncDirty(ctx context.Context) (int, error) {
	count, err := s.repository.SyncDirty(ctx, s.options.SyncBatchSize)
	if err == nil && count > 0 {
		s.invalidateSearchCache()
	}
	return count, err
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
	cacheKey := s.searchKey(query)
	if page, ok := s.getSearchCache(cacheKey); ok {
		return page, nil
	}
	flight, leader := s.beginSearchFlight(cacheKey)
	if !leader {
		select {
		case <-flight.done:
			if flight.err != nil {
				return SearchPage{}, flight.err
			}
			return cloneSearchPage(flight.page), nil
		case <-ctx.Done():
			return SearchPage{}, ctx.Err()
		}
	}
	page, err := s.repository.SearchPage(ctx, query)
	if err == nil {
		s.putSearchCache(cacheKey, page)
	}
	if flight != nil {
		s.finishSearchFlight(cacheKey, flight, page, err)
	}
	return page, err
}

// beginSearchFlight returns a flight and whether the caller is responsible for
// executing it. A nil flight means the bounded coalescing table is full, so the
// caller should bypass coalescing and execute the query independently.
func (s *Service) beginSearchFlight(key string) (*searchFlight, bool) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if flight, ok := s.searchFlights[key]; ok {
		return flight, false
	}
	if len(s.searchFlights) >= searchFlightMax {
		return nil, true
	}
	flight := &searchFlight{done: make(chan struct{})}
	s.searchFlights[key] = flight
	return flight, true
}

func (s *Service) finishSearchFlight(key string, flight *searchFlight, page SearchPage, err error) {
	s.searchMu.Lock()
	if current, ok := s.searchFlights[key]; ok && current == flight {
		flight.page = cloneSearchPage(page)
		flight.err = err
		delete(s.searchFlights, key)
		close(flight.done)
	}
	s.searchMu.Unlock()
}

func (s *Service) searchKey(query SearchQuery) string {
	s.searchMu.Lock()
	version := s.searchVersion
	s.searchMu.Unlock()
	return fmt.Sprintf("%d|%d|%s|%s|%s|%s|%s|%d|%d", s.repository.database.RenderEpoch(), version, query.Text, query.Kind, query.CategorySlug, query.TagSlug, query.Sort, query.Page, query.PerPage)
}

func (s *Service) getSearchCache(key string) (SearchPage, bool) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	element, ok := s.searchItems[key]
	if !ok {
		return SearchPage{}, false
	}
	s.searchRecent.MoveToFront(element)
	return cloneSearchPage(element.Value.(*searchCacheEntry).page), true
}

func (s *Service) putSearchCache(key string, page SearchPage) {
	size := int64(len(key) + 128)
	for _, result := range page.Results {
		size += int64(len(result.Kind) + len(result.Path) + len(result.Title) + len(result.Excerpt) + len(result.CoverMediaPublicID) + 64)
	}
	if size > searchCacheMaxBytes {
		return
	}
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if existing, ok := s.searchItems[key]; ok {
		s.searchBytes -= existing.Value.(*searchCacheEntry).size
		s.searchRecent.Remove(existing)
		delete(s.searchItems, key)
	}
	element := s.searchRecent.PushFront(&searchCacheEntry{key: key, page: cloneSearchPage(page), size: size})
	s.searchItems[key] = element
	s.searchBytes += size
	for s.searchRecent.Len() > searchCacheMaxEntries || s.searchBytes > searchCacheMaxBytes {
		oldest := s.searchRecent.Back()
		if oldest == nil {
			break
		}
		item := oldest.Value.(*searchCacheEntry)
		s.searchBytes -= item.size
		delete(s.searchItems, item.key)
		s.searchRecent.Remove(oldest)
	}
}

func (s *Service) invalidateSearchCache() {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	s.searchVersion++
	s.searchItems = make(map[string]*list.Element)
	s.searchRecent.Init()
	s.searchBytes = 0
}

func cloneSearchPage(page SearchPage) SearchPage {
	results := make([]SearchResult, len(page.Results))
	for index, result := range page.Results {
		results[index] = result
		results[index].CoverMediaPublicID = append([]byte(nil), result.CoverMediaPublicID...)
	}
	page.Results = results
	return page
}

func (s *Service) Feed(ctx context.Context) ([]FeedItem, error) {
	return s.repository.Feed(ctx, s.options.FeedLimit)
}

func (s *Service) Sitemap(ctx context.Context) ([]SitemapEntry, error) {
	return s.repository.Sitemap(ctx, s.options.SitemapLimit)
}

func (s *Service) ArchiveIndex(ctx context.Context) ([]ArchiveYear, error) {
	epoch := s.repository.database.RenderEpoch()
	s.archiveMu.RLock()
	if s.archiveEpoch == epoch && s.archive != nil {
		result := cloneArchiveYears(s.archive)
		s.archiveMu.RUnlock()
		return result, nil
	}
	s.archiveMu.RUnlock()
	result, err := s.repository.ArchiveIndex(ctx)
	if err != nil {
		return nil, err
	}
	s.archiveMu.Lock()
	if epoch == s.repository.database.RenderEpoch() {
		s.archiveEpoch = epoch
		s.archive = cloneArchiveYears(result)
	}
	s.archiveMu.Unlock()
	return result, nil
}

func cloneArchiveYears(values []ArchiveYear) []ArchiveYear {
	result := make([]ArchiveYear, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Months = append([]ArchiveMonth(nil), value.Months...)
	}
	return result
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
