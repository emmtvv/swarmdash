package admin

import (
	"net/http"

	"swarmdash/internal/store"
)

// audit records a mutating action against the store's audit log. err may be
// nil for a successful action; handlers call this right after the action
// completes (or fails) so failed attempts are recorded too.
func (s *Server) audit(r *http.Request, action, target, detail string, err error) {
	entry := store.AuditEntry{
		Username: userFromContext(r).Username,
		Action:   action,
		Target:   target,
		Detail:   detail,
		Success:  err == nil,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	if putErr := s.store.AppendAudit(entry); putErr != nil {
		s.log.Error("append audit entry", "err", putErr)
	}
}

func (s *Server) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	total, err := s.store.CountAudit()
	if err != nil {
		http.Error(w, "count audit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pagination := newPagination(r, parsePage(r), total)
	entries, err := s.store.ListAudit((pagination.Page-1)*pagination.PageSize, pagination.PageSize)
	if err != nil {
		http.Error(w, "list audit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "audit.html", map[string]any{
		"User":       userFromContext(r),
		"Entries":    entries,
		"Pagination": pagination,
	})
}
