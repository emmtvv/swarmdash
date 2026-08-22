package admin

import "net/http"

func (s *Server) handleEventsPage(w http.ResponseWriter, r *http.Request) {
	serviceFilter := r.URL.Query().Get("service")
	total, err := s.store.CountTaskEvents(serviceFilter)
	if err != nil {
		http.Error(w, "count events: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pagination := newPagination(r, parsePage(r), total)
	events, err := s.store.ListTaskEvents((pagination.Page-1)*pagination.PageSize, pagination.PageSize, serviceFilter)
	if err != nil {
		http.Error(w, "list events: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "events.html", map[string]any{
		"User":       userFromContext(r),
		"Events":     events,
		"Service":    serviceFilter,
		"Pagination": pagination,
	})
}
