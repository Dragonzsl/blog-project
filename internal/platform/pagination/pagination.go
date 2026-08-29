// Package pagination contains the small, shared contract used by public
// collections. Keeping it below the platform layer prevents publishing,
// discovery, organization, and presentation from importing one another just
// to agree on page numbers.
package pagination

type Request struct {
	Page    int
	PerPage int
}

type Info struct {
	Page        int
	PerPage     int
	Total       int
	PageCount   int
	HasPrevious bool
	HasNext     bool
}

func Normalize(request Request, defaultPerPage, maxPerPage int) Request {
	if defaultPerPage < 1 {
		defaultPerPage = 20
	}
	if maxPerPage < defaultPerPage {
		maxPerPage = defaultPerPage
	}
	if request.Page < 1 {
		request.Page = 1
	}
	if request.PerPage < 1 {
		request.PerPage = defaultPerPage
	}
	if request.PerPage > maxPerPage {
		request.PerPage = maxPerPage
	}
	return request
}

func NewInfo(total int, request Request) Info {
	if total < 0 {
		total = 0
	}
	if request.Page < 1 {
		request.Page = 1
	}
	if request.PerPage < 1 {
		request.PerPage = 20
	}
	pageCount := 0
	if total > 0 {
		pageCount = (total + request.PerPage - 1) / request.PerPage
	}
	page := request.Page
	if pageCount > 0 && page > pageCount {
		page = pageCount
	}
	if pageCount == 0 {
		page = 1
	}
	return Info{
		Page:        page,
		PerPage:     request.PerPage,
		Total:       total,
		PageCount:   pageCount,
		HasPrevious: page > 1,
		HasNext:     pageCount > 0 && page < pageCount,
	}
}

func (info Info) Offset() int {
	if info.Page < 1 || info.PerPage < 1 {
		return 0
	}
	return (info.Page - 1) * info.PerPage
}

func (info Info) Request() Request {
	return Request{Page: info.Page, PerPage: info.PerPage}
}
