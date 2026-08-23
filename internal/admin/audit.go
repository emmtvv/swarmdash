package admin

import (
	"net/http"

	"swarmdash/internal/store"
)

// audit records a mutating action against the store's audit log, attributed
// to r's authenticated session user. err may be nil for a successful
// action; handlers call this right after the action completes (or fails)
// so failed attempts are recorded too. Only call this from handlers reached
// through requireAuth - for anything that runs before a session exists
// (login itself, the SSO callback), use auditAs with an explicit username
// instead, since userFromContext(r) is nil there and .Username on it would
// panic.
func (s *Server) audit(r *http.Request, action, target, detail string, err error) {
	s.auditAs(r, userFromContext(r).Username, action, target, detail, err)
}

// auditAs is audit's building block: records a mutating action attributed
// to an explicitly given username rather than reading one from r's
// context. Used by handlers that authenticate as part of the same request
// they're auditing - local login and the SSO callback - where there's no
// prior session to attach to the context yet, so the account name has to
// come from wherever that handler just established it (the credentials
// check, the IdP's claims, ...).
func (s *Server) auditAs(r *http.Request, username, action, target, detail string, err error) {
	entry := store.AuditEntry{
		Username: username,
		Action:   action,
		Target:   target,
		Detail:   detail,
		Success:  err == nil,
		IP:       s.clientIP(r),
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
