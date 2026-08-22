package admin

import (
	"net/http"
	"net/url"
	"strconv"
)

// defaultPageSize caps how many rows a single list page renders. Several
// pages here (images, volumes, task events, the audit log) can otherwise
// grow into the thousands on a busy cluster, which made those pages slow to
// load - both the response size and the resulting DOM got huge. Paging
// keeps each response small regardless of how big the underlying list gets.
const defaultPageSize = 50

// Pagination carries paging metadata to a list template, including
// ready-to-use Prev/Next links that preserve whatever other query
// parameters the page was loaded with (e.g. ?node=X on /images).
type Pagination struct {
	Page       int
	PageSize   int
	Total      int
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
}

// parsePage reads ?page= from the request, defaulting to 1 for anything
// missing or invalid.
func parsePage(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// pageURL rebuilds the current request's URL with ?page= set to the given
// page (omitted entirely for page 1, so the default view keeps a clean URL).
func pageURL(r *http.Request, page int) string {
	q := r.URL.Query()
	if page <= 1 {
		q.Del("page")
	} else {
		q.Set("page", strconv.Itoa(page))
	}
	u := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
	return u.String()
}

// newPagination computes paging metadata for `total` items, clamping the
// requested page into range.
func newPagination(r *http.Request, page, total int) Pagination {
	totalPages := (total + defaultPageSize - 1) / defaultPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	return Pagination{
		Page:       page,
		PageSize:   defaultPageSize,
		Total:      total,
		TotalPages: totalPages,
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
		PrevURL:    pageURL(r, page-1),
		NextURL:    pageURL(r, page+1),
	}
}

// paginateSlice slices an already-fetched, in-memory list down to the
// requested page. Used by pages like images/volumes where the full list is
// fetched from a single node's agent in one call anyway - the fetch isn't
// what's slow, rendering thousands of rows at once is, so paging happens
// here rather than by teaching the agent protocol to page.
func paginateSlice[T any](r *http.Request, items []T) ([]T, Pagination) {
	p := newPagination(r, parsePage(r), len(items))
	start := (p.Page - 1) * p.PageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + p.PageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], p
}
