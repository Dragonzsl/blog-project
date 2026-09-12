package presentation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	htmlstd "html"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/discovery"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/clientip"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
	platformslug "github.com/zhushilin/blog-project/internal/platform/slug"
	"github.com/zhushilin/blog-project/internal/publishing"
)

type ContentQueries interface {
	Article(context.Context, int64) (publishing.Article, error)
	Page(context.Context, int64) (publishing.Article, error)
	PublicArticle(context.Context, string) (publishing.Article, error)
	PublishedArticles(context.Context, int) ([]publishing.Article, error)
	PublicArticlesPage(context.Context, pagination.Request) (publishing.PublicArticlePage, error)
	PublicArticleByID(context.Context, int64) (publishing.Article, error)
	PublicArticleCardsByIDs(context.Context, []int64) ([]publishing.Article, error)
	PublicArticleNavigation(context.Context, int64, int) (publishing.PublicArticleNavigation, error)
	PublicArticleNavigationForArticle(context.Context, publishing.Article, int) (publishing.PublicArticleNavigation, error)
	PublicPage(context.Context, string) (publishing.Article, error)
	PublicPageCard(context.Context, string) (publishing.Article, error)
}

type OrganizationQueries interface {
	PublicNavigation(context.Context, string) ([]organization.NavigationItem, error)
	PublicCategory(context.Context, string, int) (organization.Category, []int64, error)
	PublicTag(context.Context, string, int) (organization.Tag, []int64, error)
	PublicCategoryPage(context.Context, string, pagination.Request) (organization.PublicCategoryPage, error)
	PublicTagPage(context.Context, string, pagination.Request) (organization.PublicTagPage, error)
	PublicCategories(context.Context) ([]organization.PublicCategorySummary, error)
	PublicTags(context.Context) ([]organization.PublicTagSummary, error)
}

type SiteNamer interface {
	SiteName(context.Context) (string, error)
}

type DiscoveryQueries interface {
	BaseURL() string
	AbsoluteURL(string) string
	Search(context.Context, discovery.SearchQuery) ([]discovery.SearchResult, error)
	SearchPage(context.Context, discovery.SearchQuery) (discovery.SearchPage, error)
	Feed(context.Context) ([]discovery.FeedItem, error)
	Sitemap(context.Context) ([]discovery.SitemapEntry, error)
	ArchiveIndex(context.Context) ([]discovery.ArchiveYear, error)
	ArchiveMonthPage(context.Context, int, int, pagination.Request) (discovery.ArchivePage, error)
	ResolveRedirect(context.Context, string) (discovery.Redirect, error)
}

type AnalyticsRecorder interface {
	Record(context.Context, string, string) error
}

type FeatureProvider interface {
	Enabled(string) bool
}

type StateRepository struct {
	database *database.DB
}

func NewStateRepository(database *database.DB) *StateRepository {
	return &StateRepository{database: database}
}

func (r *StateRepository) RenderEpoch(ctx context.Context) (int64, error) {
	epoch := r.database.RenderEpoch()
	if epoch < 1 {
		return 0, fmt.Errorf("read render epoch: invalid value %d", epoch)
	}
	return epoch, nil
}

type HTTPHandler struct {
	content               ContentQueries
	siteNamer             SiteNamer
	state                 *StateRepository
	theme                 *Theme
	themeManager          *ThemeManager
	cache                 *PageCache
	flight                *RenderFlight
	searchRender          *searchRenderCache
	logger                *slog.Logger
	organization          OrganizationQueries
	discovery             DiscoveryQueries
	analytics             AnalyticsRecorder
	features              FeatureProvider
	clientIP              *clientip.Resolver
	media                 MediaQueries
	searchOptionsMu       sync.Mutex
	searchOptionsEpoch    int64
	searchOptionsReady    bool
	searchCategoryOptions []FilterOption
	searchTagOptions      []FilterOption
	searchOptionsFlight   *searchOptionsFlight
	siteMetadataMu        sync.RWMutex
	siteMetadata          SiteMetadata
}

type searchOptionsFlight struct {
	epoch      int64
	done       chan struct{}
	categories []FilterOption
	tags       []FilterOption
	err        error
}

type MediaQueries interface {
	PublicItem(context.Context, []byte) (media.Item, error)
}

type BatchMediaQueries interface {
	PublicItems(context.Context, [][]byte) ([]media.Item, error)
}

func (h *HTTPHandler) SetDiscovery(service DiscoveryQueries) { h.discovery = service }

func (h *HTTPHandler) SetThemeManager(manager *ThemeManager) { h.themeManager = manager }

func (h *HTTPHandler) SetAnalyticsRecorder(recorder AnalyticsRecorder) { h.analytics = recorder }

func (h *HTTPHandler) SetClientIPResolver(resolver *clientip.Resolver) {
	if resolver == nil {
		resolver = clientip.DirectPeerOnly()
	}
	h.clientIP = resolver
}

func (h *HTTPHandler) SetFeatureProvider(provider FeatureProvider) { h.features = provider }

func (h *HTTPHandler) SetMediaQueries(queries MediaQueries) { h.media = queries }

func (h *HTTPHandler) SetSiteMetadata(metadata SiteMetadata) {
	metadata.SocialLinks = append([]string(nil), metadata.SocialLinks...)
	h.siteMetadataMu.Lock()
	h.siteMetadata = metadata
	h.siteMetadataMu.Unlock()
}

func (h *HTTPHandler) currentTheme() *Theme {
	if h.themeManager != nil {
		return h.themeManager.Current()
	}
	return h.theme
}

func NewHTTPHandler(content ContentQueries, siteNamer SiteNamer, state *StateRepository, theme *Theme, cache *PageCache, logger *slog.Logger, organizations ...OrganizationQueries) *HTTPHandler {
	handler := &HTTPHandler{content: content, siteNamer: siteNamer, state: state, theme: theme, cache: cache, flight: NewRenderFlight(32), searchRender: newSearchRenderCache(), logger: logger}
	if len(organizations) > 0 {
		handler.organization = organizations[0]
	}
	return handler
}

func (h *HTTPHandler) RegisterPublic(router chi.Router) {
	router.NotFound(h.notFound)
	router.MethodNotAllowed(h.methodNotAllowed)
	router.Get("/", h.home)
	router.Head("/", h.home)
	router.Get("/articles", h.articles)
	router.Head("/articles", h.articles)
	router.Get("/archive", h.archiveIndex)
	router.Head("/archive", h.archiveIndex)
	router.Get("/archive/{year}/{month}", h.archiveMonth)
	router.Head("/archive/{year}/{month}", h.archiveMonth)
	router.Get("/categories", h.categoriesIndex)
	router.Head("/categories", h.categoriesIndex)
	router.Get("/posts/{slug}", h.article)
	router.Head("/posts/{slug}", h.article)
	router.Get("/categories/{slug}", h.category)
	router.Head("/categories/{slug}", h.category)
	router.Get("/tags", h.tagsIndex)
	router.Head("/tags", h.tagsIndex)
	router.Get("/tags/{slug}", h.tag)
	router.Head("/tags/{slug}", h.tag)
	router.Get("/search", h.search)
	router.Head("/search", h.search)
	router.Get("/rss.xml", h.rss)
	router.Head("/rss.xml", h.rss)
	router.Get("/sitemap.xml", h.sitemap)
	router.Head("/sitemap.xml", h.sitemap)
	router.Get("/robots.txt", h.robots)
	router.Head("/robots.txt", h.robots)
	router.Get("/llms.txt", h.llms)
	router.Head("/llms.txt", h.llms)
	router.Get("/assets/theme/{themeID}/{fingerprint}/theme.css", h.asset)
	router.Head("/assets/theme/{themeID}/{fingerprint}/theme.css", h.asset)
	router.Get("/assets/theme/{themeID}/{fingerprint}/theme.js", h.scriptAsset)
	router.Head("/assets/theme/{themeID}/{fingerprint}/theme.js", h.scriptAsset)
	router.Get("/{slug}", h.page)
	router.Head("/{slug}", h.page)
}

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/articles/{articleID}/preview", h.preview)
	router.Get("/pages/{pageID}/preview", h.preview)
}

func (h *HTTPHandler) home(w http.ResponseWriter, r *http.Request) {
	h.serveCached(w, r, "home", func(ctx context.Context) ([]byte, string, error) {
		articles, err := h.content.PublishedArticles(ctx, 20)
		if err != nil {
			return nil, "", err
		}
		cards := h.articleCardsWithMedia(ctx, articles)
		homeView := HomePageData{}
		if len(cards) > 0 {
			homeView.Featured = &cards[0]
			if len(cards) > 1 {
				homeView.Recent = cards[1:]
			}
		}
		if h.organization != nil {
			categories, err := h.organization.PublicCategories(ctx)
			if err != nil {
				return nil, "", err
			}
			homeView.Categories = termSummariesFromCategories(categories)
		}
		if h.discovery != nil {
			archive, err := h.discovery.ArchiveIndex(ctx)
			if err != nil {
				return nil, "", err
			}
			homeView.Archive = archiveMonthViews(archive, 6)
		}
		if h.content != nil {
			about, aboutErr := h.content.PublicPageCard(ctx, "about")
			if aboutErr == nil {
				homeView.About = toArticleCard(h.articleDataWithMedia(ctx, about))
			} else if !errors.Is(aboutErr, publishing.ErrNotFound) {
				return nil, "", aboutErr
			}
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx, "/")
		if err != nil {
			return nil, "", err
		}
		schema := map[string]any{
			"@context": "https://schema.org", "@type": "WebSite", "name": siteName,
			"url": h.absoluteURL("/"), "potentialAction": map[string]any{
				"@type": "SearchAction", "target": h.absoluteURL("/search") + "?q={search_term_string}",
				"query-input": "required name=search_term_string",
			},
		}
		if siteMetadata := h.currentSiteMetadata(); len(siteMetadata.SocialLinks) > 0 {
			schema["sameAs"] = siteMetadata.SocialLinks
		}
		metadata := h.metadata(siteName, siteName, h.defaultSiteDescription(siteName+"的文章与思考。"), "/", "website", schema)
		theme := h.currentTheme()
		body, err := theme.RenderHomePageWithView(siteName, homeView, navigation, metadata, cards)
		lastModified := ""
		if len(articles) > 0 && articles[0].PublishedRevisionAt != nil {
			lastModified = articles[0].PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) articles(w http.ResponseWriter, r *http.Request) {
	pageNumber := requestedPage(r)
	request := pagination.Request{Page: pageNumber, PerPage: 20}
	cacheKey := fmt.Sprintf("articles:%d", pageNumber)
	h.serveCached(w, r, cacheKey, func(ctx context.Context) ([]byte, string, error) {
		page, err := h.content.PublicArticlesPage(ctx, request)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		basePath := "/articles"
		navigation, err := h.navigation(ctx, basePath)
		if err != nil {
			return nil, "", err
		}
		metadata := h.metadata(siteName, "文章 · "+siteName, "浏览"+siteName+"的全部公开文章。", basePath, "website", nil)
		collection := CollectionView{
			Title: "文章", Description: "按发布时间浏览所有公开文章。", Items: h.articleCardsWithMedia(ctx, page.Articles),
			Pagination:   paginationView(page.Pagination, func(number int) string { return pagePath("/articles", number) }),
			CanonicalURL: h.absoluteURL("/articles"),
		}
		body, err := h.currentTheme().RenderCollectionPage(siteName, collection, navigation, metadata)
		lastModified := ""
		if len(page.Articles) > 0 && page.Articles[0].PublishedRevisionAt != nil {
			lastModified = page.Articles[0].PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) categoriesIndex(w http.ResponseWriter, r *http.Request) {
	if h.organization == nil {
		h.renderStatus(w, r, http.StatusNotFound, "分类暂不可用", "站点还没有启用内容组织能力。")
		return
	}
	h.serveCached(w, r, "categories-index", func(ctx context.Context) ([]byte, string, error) {
		categories, err := h.organization.PublicCategories(ctx)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx, "/categories")
		if err != nil {
			return nil, "", err
		}
		metadata := h.metadata(siteName, "分类 · "+siteName, "按主题浏览"+siteName+"的公开文章。", "/categories", "website", nil)
		directory := DirectoryView{
			Title:       "分类",
			Description: "按主题浏览公开文章，找到一组值得连续阅读的内容。",
			Categories:  termSummariesFromCategories(categories),
		}
		body, err := h.currentTheme().RenderDirectoryPage(siteName, directory, navigation, metadata)
		return body, "", err
	})
}

func (h *HTTPHandler) tagsIndex(w http.ResponseWriter, r *http.Request) {
	if h.organization == nil {
		h.renderStatus(w, r, http.StatusNotFound, "标签暂不可用", "站点还没有启用内容组织能力。")
		return
	}
	h.serveCached(w, r, "tags-index", func(ctx context.Context) ([]byte, string, error) {
		tags, err := h.organization.PublicTags(ctx)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx, "/tags")
		if err != nil {
			return nil, "", err
		}
		metadata := h.metadata(siteName, "标签 · "+siteName, "按关键词浏览"+siteName+"的公开文章。", "/tags", "website", nil)
		directory := DirectoryView{
			Title:       "标签",
			Description: "用关键词穿行于公开文章，发现相互连接的想法。",
			Tags:        termSummariesFromTags(tags),
		}
		body, err := h.currentTheme().RenderDirectoryPage(siteName, directory, navigation, metadata)
		return body, "", err
	})
}

func (h *HTTPHandler) archiveIndex(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		h.renderStatus(w, r, http.StatusNotFound, "归档暂不可用", "站点还没有启用公开内容发现能力。")
		return
	}
	h.serveCached(w, r, "archive-index", func(ctx context.Context) ([]byte, string, error) {
		archive, err := h.discovery.ArchiveIndex(ctx)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx, "/archive")
		if err != nil {
			return nil, "", err
		}
		metadata := h.metadata(siteName, "归档 · "+siteName, "按年份和月份浏览"+siteName+"的公开文章。", "/archive", "website", nil)
		directory := DirectoryView{
			Title:        "归档",
			Description:  "按年份和月份回看写作轨迹。",
			ArchiveYears: archiveYearViews(archive),
		}
		body, err := h.currentTheme().RenderDirectoryPage(siteName, directory, navigation, metadata)
		return body, "", err
	})
}

func (h *HTTPHandler) archiveMonth(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		h.renderStatus(w, r, http.StatusNotFound, "归档暂不可用", "站点还没有启用公开内容发现能力。")
		return
	}
	year, yearErr := strconv.Atoi(chi.URLParam(r, "year"))
	month, monthErr := strconv.Atoi(chi.URLParam(r, "month"))
	if yearErr != nil || monthErr != nil {
		h.renderStatus(w, r, http.StatusNotFound, "归档不存在", "这个时间段没有可浏览的公开文章。")
		return
	}
	pageNumber := requestedPage(r)
	request := pagination.Request{Page: pageNumber, PerPage: 20}
	basePath := archiveMonthPath(year, month)
	h.serveCached(w, r, fmt.Sprintf("archive:%d:%02d:%d", year, month, pageNumber), func(ctx context.Context) ([]byte, string, error) {
		page, err := h.discovery.ArchiveMonthPage(ctx, year, month, request)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx, basePath)
		if err != nil {
			return nil, "", err
		}
		title := fmt.Sprintf("%d 年 %02d 月", page.Year, page.Month)
		metadata := h.metadata(siteName, title+" · "+siteName, "浏览"+title+"发布的公开文章。", basePath, "website", nil)
		items := h.articleCardsFromSearchResultsWithMedia(ctx, page.Results)
		collection := CollectionView{
			Title:        title,
			Description:  fmt.Sprintf("这一时间段共发布 %d 篇文章。", page.Pagination.Total),
			Items:        items,
			Pagination:   paginationView(page.Pagination, func(number int) string { return pagePath(basePath, number) }),
			CanonicalURL: h.absoluteURL(basePath),
		}
		body, err := h.currentTheme().RenderCollectionPage(siteName, collection, navigation, metadata)
		lastModified := ""
		if len(page.Results) > 0 {
			lastModified = page.Results[0].PublishedAt.Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) article(w http.ResponseWriter, r *http.Request) {
	slug := publicSlugParam(r)
	h.serveCached(w, r, "article:"+slug, func(ctx context.Context) ([]byte, string, error) {
		article, err := h.content.PublicArticle(ctx, slug)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		path := "/posts/" + article.Slug
		navigation, err := h.navigation(ctx, path)
		if err != nil {
			return nil, "", err
		}
		metadata := h.articleMetadata(ctx, siteName, article, path)
		view := h.articleDataWithMedia(ctx, article)
		if article.Kind == "article" {
			articleNavigation, err := h.content.PublicArticleNavigationForArticle(ctx, article, 3)
			if err != nil {
				return nil, "", err
			}
			view = h.withArticleNavigation(ctx, view, articleNavigation)
		}
		theme := h.currentTheme()
		body, err := theme.RenderArticlePage(siteName, view, false, "", navigation, metadata)
		lastModified := ""
		if article.PublishedRevisionAt != nil {
			lastModified = article.PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) page(w http.ResponseWriter, r *http.Request) {
	slug := publicSlugParam(r)
	h.serveCached(w, r, "page:"+slug, func(ctx context.Context) ([]byte, string, error) {
		page, err := h.content.PublicPage(ctx, slug)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx, "/"+page.Slug)
		if err != nil {
			return nil, "", err
		}
		path := "/" + page.Slug
		metadata := h.articleMetadata(ctx, siteName, page, path)
		theme := h.currentTheme()
		body, err := theme.RenderArticlePage(siteName, h.articleDataWithMedia(ctx, page), false, "", navigation, metadata)
		lastModified := ""
		if page.PublishedRevisionAt != nil {
			lastModified = page.PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) category(w http.ResponseWriter, r *http.Request) {
	h.taxonomyListing(w, r, "category")
}
func (h *HTTPHandler) tag(w http.ResponseWriter, r *http.Request) { h.taxonomyListing(w, r, "tag") }

func (h *HTTPHandler) taxonomyListing(w http.ResponseWriter, r *http.Request, kind string) {
	if h.organization == nil {
		http.NotFound(w, r)
		return
	}
	slug := publicSlugParam(r)
	pageNumber := requestedPage(r)
	request := pagination.Request{Page: pageNumber, PerPage: 20}
	h.serveCached(w, r, fmt.Sprintf("%s:%s:%d", kind, slug, pageNumber), func(ctx context.Context) ([]byte, string, error) {
		var title, description, canonicalSlug string
		var ids []int64
		var pageInfo pagination.Info
		var err error
		if kind == "category" {
			page, pageErr := h.organization.PublicCategoryPage(ctx, slug, request)
			err = pageErr
			ids, pageInfo = page.ArticleIDs, page.Pagination
			title, description = page.Category.Name, page.Category.Description
			canonicalSlug = page.Category.Slug
		} else {
			page, pageErr := h.organization.PublicTagPage(ctx, slug, request)
			err = pageErr
			ids, pageInfo = page.ArticleIDs, page.Pagination
			title, description = "# "+page.Tag.Name, page.Tag.Description
			canonicalSlug = page.Tag.Slug
		}
		if err != nil {
			return nil, "", err
		}
		articles, err := h.content.PublicArticleCardsByIDs(ctx, ids)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		basePath := taxonomyPath(kind, canonicalSlug)
		navigation, err := h.navigation(ctx, basePath)
		if err != nil {
			return nil, "", err
		}
		metadata := h.metadata(siteName, title+" · "+siteName, description, basePath, "website", map[string]any{
			"@context": "https://schema.org", "@type": "CollectionPage", "name": title,
			"url": h.absoluteURL(basePath), "description": description,
		})
		theme := h.currentTheme()
		collection := CollectionView{Title: title, Description: description, Items: h.articleCardsWithMedia(ctx, articles), Pagination: paginationView(pageInfo, func(number int) string { return pagePath(basePath, number) }), CanonicalURL: h.absoluteURL(basePath)}
		body, err := theme.RenderCollectionPage(siteName, collection, navigation, metadata)
		lastModified := ""
		if len(articles) > 0 && articles[0].PublishedRevisionAt != nil {
			lastModified = articles[0].PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) search(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := strings.TrimSpace(r.URL.Query().Get("type"))
	categoryRaw := strings.TrimSpace(r.URL.Query().Get("category"))
	tagRaw := strings.TrimSpace(r.URL.Query().Get("tag"))
	sortOrder := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortOrder == "" {
		sortOrder = "relevance"
	}
	pageNumber := requestedPage(r)
	category, categoryValid := normalizedFilter(categoryRaw)
	tag, tagValid := normalizedFilter(tagRaw)
	epoch, err := h.state.RenderEpoch(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	theme := h.currentTheme()
	searchVersion := uint64(0)
	if versioned, ok := h.discovery.(interface{ SearchCacheVersion() uint64 }); ok {
		searchVersion = versioned.SearchCacheVersion()
	}
	cacheKey := searchRenderKey(theme.Version(), query, kind, categoryRaw, tagRaw, sortOrder, strconv.Itoa(pageNumber), strconv.FormatUint(searchVersion, 10))
	if body, ok := h.searchRender.Get(cacheKey, epoch); ok {
		h.writeSearchResponse(w, r, body, "HIT")
		return
	}
	body, _, coalesced, err := h.flight.Do(r.Context(), fmt.Sprintf("search:%d:%s", epoch, cacheKey), func(ctx context.Context) ([]byte, string, error) {
		page := SearchPageData{Query: query, Kind: kind, Category: categoryRaw, Tag: tagRaw, Sort: sortOrder}
		categoryOptions, tagOptions, err := h.searchFilterOptions(ctx)
		if err != nil {
			return nil, "", err
		}
		page.CategoryOptions = categoryOptions
		page.TagOptions = tagOptions
		if query != "" {
			if !categoryValid || !tagValid {
				page.Invalid = true
			} else {
				results, searchErr := h.discovery.SearchPage(ctx, discovery.SearchQuery{
					Text: query, Kind: kind, CategorySlug: category, TagSlug: tag, Sort: sortOrder, Page: pageNumber, PerPage: 20,
				})
				if errors.Is(searchErr, discovery.ErrInvalidQuery) {
					page.Invalid = true
				} else if searchErr != nil {
					return nil, "", searchErr
				} else {
					page.Searched = true
					page.Total = results.Pagination.Total
					page.Pagination = paginationView(results.Pagination, func(number int) string { return searchPageURL(query, kind, categoryRaw, tagRaw, sortOrder, number) })
					cards := h.articleCardsFromSearchResultsWithMedia(ctx, results.Results)
					for index, result := range results.Results {
						card := cards[index]
						card.HighlightedTitle = highlightText(result.Title, query)
						card.HighlightedExcerpt = highlightText(result.Excerpt, query)
						page.Results = append(page.Results, card)
					}
				}
			}
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx, "/search")
		if err != nil {
			return nil, "", err
		}
		metadata := h.metadata(siteName, "搜索 · "+siteName, "搜索"+siteName+"的公开文章与页面。", "/search", "website", nil)
		metadata.NoIndex = true
		body, err := theme.RenderSearch(siteName, page, navigation, metadata)
		if err == nil {
			h.searchRender.Put(cacheKey, epoch, body)
		}
		return body, "", err
	})
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	cacheStatus := "MISS"
	if coalesced {
		cacheStatus = "COALESCED"
	}
	h.writeSearchResponse(w, r, body, cacheStatus)
}

func (h *HTTPHandler) writeSearchResponse(w http.ResponseWriter, r *http.Request, body []byte, cacheStatus string) {
	setPublicSecurityHeaders(w, body)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("X-Search-Cache", cacheStatus)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

type rssDocument struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}
type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Language      string    `xml:"language"`
	LastBuildDate string    `xml:"lastBuildDate,omitempty"`
	Items         []rssItem `xml:"item"`
}
type rssItem struct {
	Title       string        `xml:"title"`
	Link        string        `xml:"link"`
	Description string        `xml:"description"`
	Published   string        `xml:"pubDate"`
	GUID        rssGUID       `xml:"guid"`
	Enclosure   *rssEnclosure `xml:"enclosure,omitempty"`
}
type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}
type rssGUID struct {
	Permalink string `xml:"isPermaLink,attr"`
	Value     string `xml:",chardata"`
}

func (h *HTTPHandler) rss(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	h.serveCachedDocument(w, r, "rss", "application/rss+xml; charset=utf-8", func(ctx context.Context) ([]byte, string, error) {
		items, err := h.discovery.Feed(ctx)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		language := "zh-CN"
		if siteMetadata := h.currentSiteMetadata(); siteMetadata.Language != "" {
			language = siteMetadata.Language
		}
		document := rssDocument{Version: "2.0", Channel: rssChannel{
			Title: siteName, Link: h.absoluteURL("/"), Description: h.defaultSiteDescription(siteName + "的最新文章。"), Language: language,
		}}
		mediaItems, mediaBatched := h.publicMediaItems(ctx, func() [][]byte {
			ids := make([][]byte, 0, len(items))
			for _, item := range items {
				ids = append(ids, item.CoverMediaPublicID)
			}
			return ids
		}())
		lastModified := ""
		var latest time.Time
		for _, item := range items {
			absolute := h.absoluteURL(item.Path)
			description := item.Excerpt
			if description == "" {
				description = item.Title
			}
			rssEntry := rssItem{
				Title: item.Title, Link: absolute, Description: description,
				Published: item.PublishedAt.Format(time.RFC1123Z), GUID: rssGUID{Permalink: "true", Value: absolute},
			}
			if h.media != nil && len(item.CoverMediaPublicID) > 0 {
				mediaItem, found := mediaItems[hex.EncodeToString(item.CoverMediaPublicID)]
				if !found && !mediaBatched {
					mediaItem, _ = h.media.PublicItem(ctx, item.CoverMediaPublicID)
					found = len(mediaItem.PublicID) > 0
				}
				if found {
					if view, viewErr := mediaItem.PublicView(); viewErr == nil {
						rssEntry.Enclosure = &rssEnclosure{URL: h.absoluteURL(view.URL), Length: mediaItem.SizeBytes, Type: mediaItem.MIMEType}
					}
				}
			}
			document.Channel.Items = append(document.Channel.Items, rssEntry)
			if item.UpdatedAt.After(latest) {
				latest = item.UpdatedAt
			}
		}
		if !latest.IsZero() {
			document.Channel.LastBuildDate = latest.Format(time.RFC1123Z)
			lastModified = latest.Format(http.TimeFormat)
		}
		body, err := xml.Marshal(document)
		return append([]byte(xml.Header), body...), lastModified, err
	})
}

type sitemapDocument struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}
type sitemapURL struct {
	Location     string `xml:"loc"`
	LastModified string `xml:"lastmod,omitempty"`
}

func (h *HTTPHandler) sitemap(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	h.serveCachedDocument(w, r, "sitemap", "application/xml; charset=utf-8", func(ctx context.Context) ([]byte, string, error) {
		entries, err := h.discovery.Sitemap(ctx)
		if err != nil {
			return nil, "", err
		}
		document := sitemapDocument{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
		lastModified := ""
		var latest time.Time
		for _, entry := range entries {
			item := sitemapURL{Location: h.absoluteURL(entry.Path)}
			if !entry.LastModified.IsZero() {
				item.LastModified = entry.LastModified.Format("2006-01-02T15:04:05Z07:00")
				if entry.LastModified.After(latest) {
					latest = entry.LastModified
					lastModified = entry.LastModified.Format(http.TimeFormat)
				}
			}
			document.URLs = append(document.URLs, item)
		}
		body, err := xml.Marshal(document)
		return append([]byte(xml.Header), body...), lastModified, err
	})
}

func (h *HTTPHandler) robots(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	body := []byte("User-agent: *\nAllow: /\nDisallow: /admin/\nDisallow: /search\nSitemap: " + h.absoluteURL("/sitemap.xml") + "\n")
	h.writeGenerated(w, r, "text/plain; charset=utf-8", body, "")
}

// searchFilterOptions keeps the public search form's taxonomy options in a
// process-local epoch snapshot. Organization already caches the source
// summaries, but cloning and converting hundreds of terms for every search
// request still creates avoidable allocation and lock pressure. The flight
// ensures the first request after an epoch change performs the work once.
func (h *HTTPHandler) searchFilterOptions(ctx context.Context) ([]FilterOption, []FilterOption, error) {
	if h.organization == nil {
		return nil, nil, nil
	}
	for {
		epoch, err := h.state.RenderEpoch(ctx)
		if err != nil {
			return nil, nil, err
		}
		h.searchOptionsMu.Lock()
		if h.searchOptionsReady && h.searchOptionsEpoch == epoch {
			categories := cloneFilterOptions(h.searchCategoryOptions)
			tags := cloneFilterOptions(h.searchTagOptions)
			h.searchOptionsMu.Unlock()
			return categories, tags, nil
		}
		if flight := h.searchOptionsFlight; flight != nil {
			done := flight.done
			matchingEpoch := flight.epoch == epoch
			h.searchOptionsMu.Unlock()
			select {
			case <-done:
				if !matchingEpoch {
					continue
				}
				if flight.err != nil {
					return nil, nil, flight.err
				}
				return cloneFilterOptions(flight.categories), cloneFilterOptions(flight.tags), nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		flight := &searchOptionsFlight{epoch: epoch, done: make(chan struct{})}
		h.searchOptionsFlight = flight
		h.searchOptionsMu.Unlock()

		loadContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		categories, loadErr := h.organization.PublicCategories(loadContext)
		var tags []organization.PublicTagSummary
		if loadErr == nil {
			tags, loadErr = h.organization.PublicTags(loadContext)
		}
		cancel()
		var categoryOptions, tagOptions []FilterOption
		if loadErr == nil {
			categoryOptions = filterOptionsFromCategories(categories)
			tagOptions = filterOptionsFromTags(tags)
		}
		h.searchOptionsMu.Lock()
		if loadErr == nil {
			h.searchOptionsEpoch = epoch
			h.searchOptionsReady = true
			h.searchCategoryOptions = cloneFilterOptions(categoryOptions)
			h.searchTagOptions = cloneFilterOptions(tagOptions)
		}
		flight.categories = cloneFilterOptions(categoryOptions)
		flight.tags = cloneFilterOptions(tagOptions)
		flight.err = loadErr
		if h.searchOptionsFlight == flight {
			h.searchOptionsFlight = nil
		}
		close(flight.done)
		h.searchOptionsMu.Unlock()
		return categoryOptions, tagOptions, loadErr
	}
}

func cloneFilterOptions(values []FilterOption) []FilterOption {
	return append([]FilterOption(nil), values...)
}

func (h *HTTPHandler) llms(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	h.serveCachedDocument(w, r, "llms", "text/plain; charset=utf-8", func(ctx context.Context) ([]byte, string, error) {
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		items, err := h.discovery.Feed(ctx)
		if err != nil {
			return nil, "", err
		}

		var document strings.Builder
		document.WriteString("# ")
		document.WriteString(llmsText(siteName))
		document.WriteString("\n\n> 这是一个安静发布文章与思考的个人博客。\n\n")
		document.WriteString("## 内容\n\n")
		lastModified := ""
		var latest time.Time
		written := 0
		for _, item := range items {
			if strings.TrimSpace(item.Path) == "" || strings.TrimSpace(item.Title) == "" {
				continue
			}
			document.WriteString("- [")
			document.WriteString(llmsLinkText(item.Title))
			document.WriteString("](<")
			document.WriteString(h.absoluteURL(item.Path))
			document.WriteString(">)")
			if excerpt := llmsText(item.Excerpt); excerpt != "" {
				document.WriteString(": ")
				document.WriteString(excerpt)
			}
			document.WriteByte('\n')
			written++
			if item.UpdatedAt.After(latest) {
				latest = item.UpdatedAt
			}
		}
		if written == 0 {
			document.WriteString("- 暂无已发布文章。\n")
		}
		document.WriteString("\n## 机器可读资源\n\n")
		document.WriteString("- [RSS](<")
		document.WriteString(h.absoluteURL("/rss.xml"))
		document.WriteString(">)\n")
		document.WriteString("- [Sitemap](<")
		document.WriteString(h.absoluteURL("/sitemap.xml"))
		document.WriteString(">)\n")
		if !latest.IsZero() {
			lastModified = latest.Format(http.TimeFormat)
		}
		return []byte(document.String()), lastModified, nil
	})
}

func llmsText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func llmsLinkText(value string) string {
	value = llmsText(value)
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "[", `\[`)
	return strings.ReplaceAll(value, "]", `\]`)
}

func (h *HTTPHandler) preview(w http.ResponseWriter, r *http.Request) {
	id, kind, err := publishingContentID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	article, err := h.content.Article(r.Context(), id)
	if kind == "page" {
		article, err = h.content.Page(r.Context(), id)
	}
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	backURL := fmt.Sprintf("/admin/articles/%d/edit", article.ID)
	if kind == "page" {
		backURL = fmt.Sprintf("/admin/pages/%d/edit", article.ID)
	}
	navigation, err := h.navigation(r.Context(), "")
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	previewData := h.articleDataWithMedia(r.Context(), article)
	// The preview body comes from the mutable editing snapshot. It may share
	// the published revision ID with the public article, so it must never use
	// the immutable public Markdown cache key.
	previewData.BodyCacheKey = ""
	body, err := h.currentTheme().RenderArticlePage(siteName, previewData, true, backURL, navigation, PageMetadata{NoIndex: true})
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	setPublicSecurityHeaders(w, body)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (h *HTTPHandler) asset(w http.ResponseWriter, r *http.Request) {
	theme := h.currentTheme()
	if theme == nil || chi.URLParam(r, "themeID") != theme.ThemeID() || chi.URLParam(r, "fingerprint") != theme.AssetHash() {
		http.NotFound(w, r)
		return
	}
	etag := `"` + theme.AssetHash() + `"`
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(theme.CSS())
	}
}

func (h *HTTPHandler) scriptAsset(w http.ResponseWriter, r *http.Request) {
	theme := h.currentTheme()
	if theme == nil || theme.ScriptHash() == "" || chi.URLParam(r, "themeID") != theme.ThemeID() || chi.URLParam(r, "fingerprint") != theme.ScriptHash() {
		http.NotFound(w, r)
		return
	}
	etag := `"` + theme.ScriptHash() + `"`
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(theme.JS())
	}
}

func (h *HTTPHandler) serveCached(w http.ResponseWriter, r *http.Request, semanticKey string, render func(context.Context) ([]byte, string, error)) {
	h.serveCachedDocument(w, r, semanticKey, "text/html; charset=utf-8", render)
}

func (h *HTTPHandler) serveCachedDocument(w http.ResponseWriter, r *http.Request, semanticKey, contentType string, render func(context.Context) ([]byte, string, error)) {
	epoch, err := h.state.RenderEpoch(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	key := h.currentTheme().Version() + "|" + semanticKey
	if cached, ok := h.cache.Get(key, epoch); ok {
		h.writeCacheEntry(w, r, cached, "HIT")
		h.recordAnalytics(r)
		return
	}
	body, lastModified, coalesced, err := h.flight.Do(r.Context(), fmt.Sprintf("%d|%s", epoch, key), render)
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	digest := sha256.Sum256(body)
	entry := CacheEntry{
		Key:          key,
		Epoch:        epoch,
		Status:       http.StatusOK,
		ContentType:  contentType,
		ETag:         `"` + hex.EncodeToString(digest[:16]) + `"`,
		LastModified: lastModified,
		Body:         body,
	}
	if !coalesced {
		if err := h.cache.PutAsync(entry); err != nil {
			h.logger.WarnContext(r.Context(), "store public page cache", "error", err, "key", semanticKey)
		}
		if h.cache.PruneDue(time.Now().UTC(), 30*time.Second) {
			go func() {
				if err := h.cache.Prune(epoch); err != nil {
					h.logger.Warn("prune public page cache", "error", err)
				}
			}()
		}
	}
	cacheStatus := "MISS"
	if coalesced {
		cacheStatus = "COALESCED"
	}
	h.writeCacheEntry(w, r, entry, cacheStatus)
	h.recordAnalytics(r)
}

func (h *HTTPHandler) recordAnalytics(r *http.Request) {
	if h.analytics == nil || r.Method != http.MethodGet {
		return
	}
	if err := h.analytics.Record(r.Context(), r.URL.Path, h.resolvedAnalyticsVisitor(r)); err != nil {
		h.logger.DebugContext(r.Context(), "record public analytics", "error", err)
	}
}

func analyticsVisitor(r *http.Request) string {
	return clientip.DirectPeerOnly().Resolve(r) + "\x00" + strings.TrimSpace(r.UserAgent())
}

func (h *HTTPHandler) resolvedAnalyticsVisitor(r *http.Request) string {
	resolver := h.clientIP
	if resolver == nil {
		resolver = clientip.DirectPeerOnly()
	}
	return resolver.Resolve(r) + "\x00" + strings.TrimSpace(r.UserAgent())
}

func (h *HTTPHandler) writeCacheEntry(w http.ResponseWriter, r *http.Request, entry CacheEntry, cacheStatus string) {
	if strings.HasPrefix(entry.ContentType, "text/html") {
		setPublicSecurityHeaders(w, entry.Body)
	} else {
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.Header().Set("Content-Type", entry.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("ETag", entry.ETag)
	w.Header().Set("X-Page-Cache", cacheStatus)
	if entry.LastModified != "" {
		w.Header().Set("Last-Modified", entry.LastModified)
	}
	if matchesETag(r.Header.Get("If-None-Match"), entry.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if entry.LastModified != "" && notModifiedSince(r, entry.LastModified) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(entry.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(entry.Body)
	}
}

func (h *HTTPHandler) writeGenerated(w http.ResponseWriter, r *http.Request, contentType string, body []byte, lastModified string) {
	digest := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(digest[:16]) + `"`
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if lastModified != "" {
		w.Header().Set("Last-Modified", lastModified)
	}
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if lastModified != "" && notModifiedSince(r, lastModified) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (h *HTTPHandler) handleRenderError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, publishing.ErrNotFound) {
		if h.tryRedirect(w, r) {
			return
		}
		h.renderStatus(w, r, http.StatusNotFound, "页面不存在", "这篇内容可能已被移动、下线，或者地址还没有发布。")
		return
	}
	if errors.Is(err, organization.ErrNotFound) {
		if h.tryRedirect(w, r) {
			return
		}
		h.renderStatus(w, r, http.StatusNotFound, "页面不存在", "这个分类、标签或导航地址暂时没有可公开浏览的内容。")
		return
	}
	if errors.Is(err, discovery.ErrNotFound) {
		if h.tryRedirect(w, r) {
			return
		}
		h.renderStatus(w, r, http.StatusNotFound, "归档不存在", "这个时间段没有可浏览的公开文章。")
		return
	}
	h.logger.ErrorContext(r.Context(), "render public page", "error", err)
	h.renderStatus(w, r, http.StatusInternalServerError, "页面暂时无法打开", "站点遇到了一点问题，请稍后再试。")
}

func (h *HTTPHandler) notFound(w http.ResponseWriter, r *http.Request) {
	if h.tryRedirect(w, r) {
		return
	}
	h.renderStatus(w, r, http.StatusNotFound, "页面不存在", "这篇内容可能已被移动、下线，或者地址还没有发布。")
}

func (h *HTTPHandler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.renderStatus(w, r, http.StatusMethodNotAllowed, "暂不支持此操作", "这个地址不支持当前请求方式。")
}

func (h *HTTPHandler) renderStatus(w http.ResponseWriter, r *http.Request, statusCode int, title, message string) {
	siteName := "个人博客"
	if h.siteNamer != nil {
		if value, err := h.siteNamer.SiteName(r.Context()); err == nil && strings.TrimSpace(value) != "" {
			siteName = value
		}
	}
	navigation := Navigation{CurrentPath: r.URL.Path, Features: h.featureFlags()}
	if h.organization != nil {
		if value, err := h.navigation(r.Context(), r.URL.Path); err == nil {
			navigation = value
		}
	}
	metadata := h.metadata(siteName, fmt.Sprintf("%d · %s", statusCode, title), message, r.URL.Path, "website", nil)
	metadata.NoIndex = true
	theme := h.currentTheme()
	if theme == nil {
		http.Error(w, http.StatusText(statusCode), statusCode)
		return
	}
	body, err := theme.RenderStatusPage(siteName, StatusView{Code: statusCode, Title: title, Message: message}, navigation, metadata)
	if err != nil {
		http.Error(w, http.StatusText(statusCode), statusCode)
		return
	}
	setPublicSecurityHeaders(w, body)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func setPublicSecurityHeaders(w http.ResponseWriter, bodies ...[]byte) {
	scriptSources := make([]string, 0, 2)
	if len(bodies) > 0 {
		body := string(bodies[0])
		if strings.Contains(body, `<script src="`) {
			scriptSources = append(scriptSources, "'self'")
		}
		scriptSources = appendInlineScriptHashes(scriptSources, bodies[0])
	}
	if len(scriptSources) == 0 {
		scriptSources = append(scriptSources, "'none'")
	}
	scriptPolicy := "script-src " + strings.Join(scriptSources, " ") + "; "
	w.Header().Set("Content-Security-Policy", "default-src 'none'; "+scriptPolicy+"style-src 'self'; img-src 'self' data: https:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func appendInlineScriptHashes(sources []string, body []byte) []string {
	markup := string(body)
	for cursor := 0; cursor < len(markup); {
		startOffset := strings.Index(markup[cursor:], "<script")
		if startOffset < 0 {
			break
		}
		start := cursor + startOffset
		endTagOffset := strings.Index(markup[start:], ">")
		if endTagOffset < 0 {
			break
		}
		contentStart := start + endTagOffset + 1
		closeOffset := strings.Index(markup[contentStart:], "</script>")
		if closeOffset < 0 {
			break
		}
		closeStart := contentStart + closeOffset
		tag := strings.ToLower(markup[start:contentStart])
		if !strings.Contains(tag, "src=") {
			digest := sha256.Sum256(body[contentStart:closeStart])
			source := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
			if !containsString(sources, source) {
				sources = append(sources, source)
			}
		}
		cursor = closeStart + len("</script>")
	}
	return sources
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (h *HTTPHandler) absoluteURL(path string) string {
	if h.discovery == nil {
		return ""
	}
	return h.discovery.AbsoluteURL(path)
}

func (h *HTTPHandler) metadata(siteName, title, description, path, openGraphType string, schema map[string]any) PageMetadata {
	siteMetadata := h.currentSiteMetadata()
	if title == siteName && siteMetadata.DefaultSEOTitle != "" {
		title = siteMetadata.DefaultSEOTitle
	}
	if description == "" {
		description = siteMetadata.DefaultSEODescription
		if description == "" {
			description = siteMetadata.Description
		}
	}
	metadata := PageMetadata{Title: title, Description: description, OpenGraphType: openGraphType, SiteName: siteName, ImageURL: siteMetadata.DefaultSocialImageURL, TwitterCard: "summary"}
	if metadata.ImageURL != "" {
		metadata.TwitterCard = "summary_large_image"
	}
	if h.discovery == nil {
		return metadata
	}
	metadata.CanonicalURL = h.absoluteURL(path)
	metadata.RSSURL = h.absoluteURL("/rss.xml")
	if schema != nil {
		metadata.JSONLD = marshalJSONLD(schema)
	}
	return metadata
}

func (h *HTTPHandler) articleMetadata(ctx context.Context, siteName string, article publishing.Article, path string) PageMetadata {
	title := article.SEOTitle
	if title == "" {
		title = article.Title + " · " + siteName
	}
	description := article.SEODescription
	if description == "" {
		description = article.Excerpt
	}
	if description == "" {
		description = article.Title
	}
	schemaType, openGraphType := "WebPage", "website"
	if article.Kind == "article" {
		schemaType, openGraphType = "BlogPosting", "article"
	}
	schema := map[string]any{
		"@context": "https://schema.org", "@type": schemaType, "headline": article.Title,
		"description": description, "url": h.absoluteURL(path),
		"isPartOf": map[string]any{"@type": "WebSite", "name": siteName, "url": h.absoluteURL("/")},
	}
	metadata := h.metadata(siteName, title, description, path, openGraphType, schema)
	if article.PublishedAt != nil {
		schema["datePublished"] = article.PublishedAt.Format(time.RFC3339)
		metadata.PublishedAt = article.PublishedAt
	}
	if article.PublishedRevisionAt != nil {
		schema["dateModified"] = article.PublishedRevisionAt.Format(time.RFC3339)
		metadata.ModifiedAt = article.PublishedRevisionAt
	}
	if h.media != nil && len(article.CoverMediaPublicID) > 0 {
		if item, err := h.media.PublicItem(ctx, article.CoverMediaPublicID); err == nil {
			if view, err := item.PublicView(); err == nil {
				metadata.ImageURL = h.absoluteURL(view.URL)
				metadata.TwitterCard = "summary_large_image"
				schema["image"] = metadata.ImageURL
			}
		}
	}
	if siteMetadata := h.currentSiteMetadata(); len(siteMetadata.SocialLinks) > 0 {
		schema["sameAs"] = siteMetadata.SocialLinks
	}
	metadata.JSONLD = marshalJSONLD(schema)
	return metadata
}

func (h *HTTPHandler) currentSiteMetadata() SiteMetadata {
	h.siteMetadataMu.RLock()
	defer h.siteMetadataMu.RUnlock()
	metadata := h.siteMetadata
	metadata.SocialLinks = append([]string(nil), metadata.SocialLinks...)
	return metadata
}

func (h *HTTPHandler) defaultSiteDescription(fallback string) string {
	metadata := h.currentSiteMetadata()
	if metadata.DefaultSEODescription != "" {
		return metadata.DefaultSEODescription
	}
	if metadata.Description != "" {
		return metadata.Description
	}
	return fallback
}

func marshalJSONLD(value any) template.JS {
	encoded, err := json.Marshal(value)
	if err != nil {
		return template.JS("")
	}
	return template.JS(encoded)
}

func notModifiedSince(r *http.Request, lastModified string) bool {
	value := strings.TrimSpace(r.Header.Get("If-Modified-Since"))
	if value == "" {
		return false
	}
	since, err := http.ParseTime(value)
	if err != nil {
		return false
	}
	modified, err := http.ParseTime(lastModified)
	return err == nil && !modified.After(since)
}

func normalizedFilter(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	_, key, err := platformslug.Normalize(value)
	return key, err == nil
}

func (h *HTTPHandler) tryRedirect(w http.ResponseWriter, r *http.Request) bool {
	if h.discovery == nil {
		return false
	}
	pathKey, ok := normalizedPathKey(r.URL.Path)
	if !ok {
		return false
	}
	redirect, err := h.discovery.ResolveRedirect(r.Context(), pathKey)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.ErrorContext(r.Context(), "resolve public redirect", "error", err, "path", r.URL.Path)
		}
		return false
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.Redirect(w, r, redirect.TargetPath, redirect.StatusCode)
	return true
}

func normalizedPathKey(path string) (string, bool) {
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return "", false
	}
	parts := strings.Split(strings.Trim(decoded, "/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		_, key, err := platformslug.Normalize(parts[0])
		return "/" + key, err == nil
	}
	if len(parts) == 2 && (parts[0] == "posts" || parts[0] == "categories" || parts[0] == "tags") {
		_, key, err := platformslug.Normalize(parts[1])
		return "/" + parts[0] + "/" + key, err == nil
	}
	return "", false
}

func publicSlugParam(r *http.Request) string {
	raw := chi.URLParam(r, "slug")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return decoded
}

func matchesETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func articleData(article publishing.Article) ArticleData {
	slug := article.Slug
	if article.PublishedSlug != "" {
		slug = article.PublishedSlug
	}
	data := ArticleData{
		Kind:         article.Kind,
		Title:        article.Title,
		Slug:         slug,
		Excerpt:      article.Excerpt,
		BodyMarkdown: article.BodyMarkdown,
		PublishedAt:  article.PublishedAt,
		UpdatedAt:    article.PublishedRevisionAt,
		ReadingTime:  readingMinutes(article.BodyMarkdown),
	}
	if article.Category != nil {
		data.Category = &TermData{Name: article.Category.Name, URL: "/categories/" + article.Category.Slug}
	}
	for _, tag := range article.Tags {
		data.Tags = append(data.Tags, TermData{Name: tag.Name, URL: "/tags/" + tag.Slug})
	}
	return data
}

func (h *HTTPHandler) articleDataWithMedia(ctx context.Context, article publishing.Article) ArticleData {
	data := articleData(article)
	if article.PublishedRevisionID > 0 {
		data.BodyCacheKey = fmt.Sprintf("%s|revision:%d", MarkdownRendererVersion, article.PublishedRevisionID)
	}
	if h.media == nil || len(article.CoverMediaPublicID) == 0 {
		return data
	}
	item, err := h.media.PublicItem(ctx, article.CoverMediaPublicID)
	if err != nil {
		h.logger.WarnContext(ctx, "resolve public cover media", "error", err)
		return data
	}
	view, err := item.PublicView()
	if err != nil {
		h.logger.WarnContext(ctx, "build public cover media view", "error", err)
		return data
	}
	data.Cover = &MediaData{URL: view.URL, Alt: view.Alt, Width: view.Width, Height: view.Height, SrcSet: view.SrcSet}
	return data
}

func (h *HTTPHandler) articleDataListWithMedia(ctx context.Context, articles []publishing.Article) []ArticleData {
	result := make([]ArticleData, 0, len(articles))
	for _, article := range articles {
		result = append(result, articleData(article))
	}
	items, batched := h.publicMediaItems(ctx, coverIDs(articles))
	if batched {
		for index, article := range articles {
			if item, ok := items[hex.EncodeToString(article.CoverMediaPublicID)]; ok {
				h.applyMediaView(&result[index], item)
			}
		}
		return result
	}
	for index, article := range articles {
		result[index] = h.articleDataWithMedia(ctx, article)
	}
	return result
}

func (h *HTTPHandler) articleCardsWithMedia(ctx context.Context, articles []publishing.Article) []ArticleCard {
	return articleCards(h.articleDataListWithMedia(ctx, articles))
}

func (h *HTTPHandler) articleCardFromSearchResult(ctx context.Context, result discovery.SearchResult) ArticleCard {
	card := articleCardFromSearchResult(result)
	if h.media == nil || len(result.CoverMediaPublicID) == 0 {
		return card
	}
	item, err := h.media.PublicItem(ctx, result.CoverMediaPublicID)
	if err != nil {
		h.logger.WarnContext(ctx, "resolve search cover media", "error", err)
		return card
	}
	view, err := item.PublicView()
	if err == nil {
		card.Cover = &MediaData{URL: view.URL, Alt: view.Alt, Width: view.Width, Height: view.Height, SrcSet: view.SrcSet}
	}
	return card
}

func (h *HTTPHandler) articleCardsFromSearchResultsWithMedia(ctx context.Context, results []discovery.SearchResult) []ArticleCard {
	cards := make([]ArticleCard, 0, len(results))
	ids := make([][]byte, 0, len(results))
	for _, result := range results {
		cards = append(cards, articleCardFromSearchResult(result))
		ids = append(ids, result.CoverMediaPublicID)
	}
	items, batched := h.publicMediaItems(ctx, ids)
	if batched {
		for index, result := range results {
			if item, ok := items[hex.EncodeToString(result.CoverMediaPublicID)]; ok {
				h.applyMediaCardView(&cards[index], item)
			}
		}
		return cards
	}
	for index, result := range results {
		cards[index] = h.articleCardFromSearchResult(ctx, result)
	}
	return cards
}

func (h *HTTPHandler) publicMediaItems(ctx context.Context, publicIDs [][]byte) (map[string]media.Item, bool) {
	if h.media == nil {
		return nil, false
	}
	batcher, ok := h.media.(BatchMediaQueries)
	if !ok {
		return nil, false
	}
	resolved := false
	for _, publicID := range publicIDs {
		if len(publicID) > 0 {
			resolved = true
			break
		}
	}
	if !resolved {
		return nil, true
	}
	items, err := batcher.PublicItems(ctx, publicIDs)
	if err != nil {
		h.logger.WarnContext(ctx, "batch resolve public media", "error", err)
		return nil, false
	}
	result := make(map[string]media.Item, len(items))
	for _, item := range items {
		result[hex.EncodeToString(item.PublicID)] = item
	}
	return result, true
}

func coverIDs(articles []publishing.Article) [][]byte {
	ids := make([][]byte, 0, len(articles))
	for _, article := range articles {
		ids = append(ids, article.CoverMediaPublicID)
	}
	return ids
}

func (h *HTTPHandler) applyMediaView(data *ArticleData, item media.Item) {
	view, err := item.PublicView()
	if err != nil {
		return
	}
	data.Cover = &MediaData{URL: view.URL, Alt: view.Alt, Width: view.Width, Height: view.Height, SrcSet: view.SrcSet}
}

func (h *HTTPHandler) applyMediaCardView(card *ArticleCard, item media.Item) {
	view, err := item.PublicView()
	if err != nil {
		return
	}
	card.Cover = &MediaData{URL: view.URL, Alt: view.Alt, Width: view.Width, Height: view.Height, SrcSet: view.SrcSet}
}

func articleDataList(articles []publishing.Article) []ArticleData {
	result := make([]ArticleData, 0, len(articles))
	for _, article := range articles {
		result = append(result, articleData(article))
	}
	return result
}

func publishingContentID(r *http.Request) (int64, string, error) {
	kind := "article"
	value := chi.URLParam(r, "articleID")
	if value == "" {
		kind = "page"
		value = chi.URLParam(r, "pageID")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return 0, "", publishing.ErrNotFound
	}
	return id, kind, nil
}

func (h *HTTPHandler) navigation(ctx context.Context, currentPath string) (Navigation, error) {
	navigation := Navigation{CurrentPath: currentPath, Features: h.featureFlags()}
	if h.organization == nil {
		return navigation, nil
	}
	primary, err := h.organization.PublicNavigation(ctx, "primary")
	if err != nil {
		return Navigation{}, err
	}
	footer, err := h.organization.PublicNavigation(ctx, "footer")
	if err != nil {
		return Navigation{}, err
	}
	navigation.Primary = navigationLinks(primary)
	navigation.Footer = navigationLinks(footer)
	navigation.Sidebar = sidebarView(currentPath, navigation.Primary, navigation.Footer)
	return navigation, nil
}

func (h *HTTPHandler) featureFlags() FeatureFlags {
	if h.features == nil {
		return FeatureFlags{}
	}
	commentsMode := ""
	if h.features.Enabled("comments.local") {
		commentsMode = "local"
	} else if h.features.Enabled("comments.external") {
		commentsMode = "external"
	}
	return FeatureFlags{
		Comments:     commentsMode != "",
		CommentsMode: commentsMode,
		Newsletter:   h.features.Enabled("newsletter.local") || h.features.Enabled("newsletter.external"),
		Analytics:    h.features.Enabled("analytics.local"),
	}
}

func requestedPage(r *http.Request) int {
	page, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page")))
	if err != nil || page < 1 {
		return 1
	}
	if page > 100_000 {
		return 100_000
	}
	return page
}

func pagePath(base string, page int) string {
	if page <= 1 {
		return base
	}
	return base + "?page=" + strconv.Itoa(page) + "#main-content"
}

func taxonomyPath(kind, slug string) string {
	if kind == "category" {
		return "/categories/" + slug
	}
	return "/tags/" + slug
}

func archiveMonthPath(year, month int) string {
	return fmt.Sprintf("/archive/%04d/%02d", year, month)
}

func termSummariesFromCategories(values []organization.PublicCategorySummary) []TermSummary {
	result := make([]TermSummary, 0, len(values))
	for _, value := range values {
		result = append(result, TermSummary{
			Name:          value.Category.Name,
			Slug:          value.Category.Slug,
			Description:   value.Category.Description,
			URL:           "/categories/" + value.Category.Slug,
			ArticleCount:  value.ArticleCount,
			LatestTitle:   value.LatestTitle,
			LatestPath:    value.LatestPath,
			LatestPublish: formatDate(value.LatestPublish),
		})
	}
	return result
}

func termSummariesFromTags(values []organization.PublicTagSummary) []TermSummary {
	result := make([]TermSummary, 0, len(values))
	for _, value := range values {
		result = append(result, TermSummary{
			Name:          value.Tag.Name,
			Slug:          value.Tag.Slug,
			Description:   value.Tag.Description,
			URL:           "/tags/" + value.Tag.Slug,
			ArticleCount:  value.ArticleCount,
			LatestTitle:   value.LatestTitle,
			LatestPath:    value.LatestPath,
			LatestPublish: formatDate(value.LatestPublish),
		})
	}
	return result
}

func filterOptionsFromCategories(values []organization.PublicCategorySummary) []FilterOption {
	result := make([]FilterOption, 0, len(values))
	for _, value := range values {
		result = append(result, FilterOption{Slug: value.Category.Slug, Name: value.Category.Name})
	}
	return result
}

func filterOptionsFromTags(values []organization.PublicTagSummary) []FilterOption {
	result := make([]FilterOption, 0, len(values))
	for _, value := range values {
		result = append(result, FilterOption{Slug: value.Tag.Slug, Name: value.Tag.Name})
	}
	return result
}

func archiveMonthViews(values []discovery.ArchiveYear, limit int) []ArchiveMonthView {
	result := make([]ArchiveMonthView, 0)
	for _, year := range values {
		for _, month := range year.Months {
			result = append(result, ArchiveMonthView{
				Year: year.Year, Month: month.Month, Label: fmt.Sprintf("%d 年 %02d 月", year.Year, month.Month),
				Count: month.Count, URL: archiveMonthPath(year.Year, month.Month),
			})
			if limit > 0 && len(result) >= limit {
				return result
			}
		}
	}
	return result
}

func archiveYearViews(values []discovery.ArchiveYear) []ArchiveYearView {
	result := make([]ArchiveYearView, 0, len(values))
	for _, year := range values {
		view := ArchiveYearView{Year: year.Year, Total: year.Total, Months: make([]ArchiveMonthView, 0, len(year.Months))}
		for _, month := range year.Months {
			view.Months = append(view.Months, ArchiveMonthView{
				Year: year.Year, Month: month.Month, Label: fmt.Sprintf("%02d 月", month.Month),
				Count: month.Count, URL: archiveMonthPath(year.Year, month.Month),
			})
		}
		result = append(result, view)
	}
	return result
}

func articleCardFromSearchResult(result discovery.SearchResult) ArticleCard {
	card := ArticleCard{
		Kind: result.Kind, Path: result.Path, Title: result.Title, Excerpt: result.Excerpt,
		PublishedAt: result.PublishedAt.Format("2006年01月02日"), PublishedISO: result.PublishedAt.Format(time.RFC3339),
	}
	return card
}

func (h *HTTPHandler) withArticleNavigation(ctx context.Context, data ArticleData, navigation publishing.PublicArticleNavigation) ArticleData {
	articles := make([]publishing.Article, 0, 2+len(navigation.Related))
	previousIndex, nextIndex := -1, -1
	if navigation.Previous != nil {
		previousIndex = len(articles)
		articles = append(articles, *navigation.Previous)
	}
	if navigation.Next != nil {
		nextIndex = len(articles)
		articles = append(articles, *navigation.Next)
	}
	relatedStart := len(articles)
	articles = append(articles, navigation.Related...)
	if len(articles) > 0 {
		cards := articleCards(h.articleDataListWithMedia(ctx, articles))
		if previousIndex >= 0 {
			data.Previous = &cards[previousIndex]
		}
		if nextIndex >= 0 {
			data.Next = &cards[nextIndex]
		}
		if relatedStart < len(cards) {
			data.Related = cards[relatedStart:]
		}
	}
	return data
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006年01月02日")
}

func highlightText(value, query string) template.HTML {
	if value == "" {
		return ""
	}
	terms := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, term := range strings.Fields(query) {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, regexp.QuoteMeta(term))
		if len(terms) >= 8 {
			break
		}
	}
	if len(terms) == 0 {
		return template.HTML(htmlstd.EscapeString(value))
	}
	re, err := regexp.Compile("(?i)(" + strings.Join(terms, "|") + ")")
	if err != nil {
		return template.HTML(htmlstd.EscapeString(value))
	}
	locations := re.FindAllStringIndex(value, -1)
	if len(locations) == 0 {
		return template.HTML(htmlstd.EscapeString(value))
	}
	var output strings.Builder
	last := 0
	for _, location := range locations {
		output.WriteString(htmlstd.EscapeString(value[last:location[0]]))
		output.WriteString("<mark>")
		output.WriteString(htmlstd.EscapeString(value[location[0]:location[1]]))
		output.WriteString("</mark>")
		last = location[1]
	}
	output.WriteString(htmlstd.EscapeString(value[last:]))
	return template.HTML(output.String())
}

func searchPageURL(query, kind, category, tag, sortOrder string, page int) string {
	values := url.Values{}
	values.Set("q", query)
	if kind != "" {
		values.Set("type", kind)
	}
	if category != "" {
		values.Set("category", category)
	}
	if tag != "" {
		values.Set("tag", tag)
	}
	if sortOrder != "" && sortOrder != "relevance" {
		values.Set("sort", sortOrder)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	return "/search?" + values.Encode() + "#search-results"
}

func paginationView(info pagination.Info, makeURL func(int) string) PaginationView {
	view := PaginationView{Page: info.Page, PerPage: info.PerPage, Total: info.Total, PageCount: info.PageCount, HasPrevious: info.HasPrevious, HasNext: info.HasNext}
	if info.HasPrevious {
		view.PreviousURL = makeURL(info.Page - 1)
	}
	if info.HasNext {
		view.NextURL = makeURL(info.Page + 1)
	}
	if info.PageCount < 1 {
		return view
	}
	numbers := map[int]struct{}{1: {}, info.PageCount: {}}
	for number := info.Page - 2; number <= info.Page+2; number++ {
		if number > 0 && number <= info.PageCount {
			numbers[number] = struct{}{}
		}
	}
	ordered := make([]int, 0, len(numbers))
	for number := range numbers {
		ordered = append(ordered, number)
	}
	sort.Ints(ordered)
	for _, number := range ordered {
		view.Pages = append(view.Pages, PageLink{Number: number, URL: makeURL(number), Current: number == info.Page})
	}
	return view
}

func withArticleNavigation(data ArticleData, navigation publishing.PublicArticleNavigation) ArticleData {
	if navigation.Previous != nil {
		data.Previous = toArticleCard(articleData(*navigation.Previous))
	}
	if navigation.Next != nil {
		data.Next = toArticleCard(articleData(*navigation.Next))
	}
	if len(navigation.Related) > 0 {
		data.Related = articleCards(articleDataList(navigation.Related))
	}
	return data
}

func navigationLinks(items []organization.NavigationItem) []NavigationLink {
	links := make(map[int64]*NavigationLink, len(items))
	roots := make([]*NavigationLink, 0, len(items))
	for _, item := range items {
		link := &NavigationLink{Label: item.Label, URL: item.URL, External: item.TargetKind == "external"}
		links[item.ID] = link
		if item.ParentID == 0 {
			roots = append(roots, link)
		} else if parent := links[item.ParentID]; parent != nil {
			parent.Children = append(parent.Children, *link)
		}
	}
	result := make([]NavigationLink, 0, len(roots))
	for _, link := range roots {
		result = append(result, *link)
	}
	return result
}
