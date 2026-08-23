// Package httpx holds tiny HTTP helpers shared by the agent and admin
// servers.
package httpx

import (
	"encoding/json"
	"mime"
	"net/http"
)

func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func ReadJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// SetAttachment sets Content-Disposition for a file download, deriving the
// quoting/escaping (and the RFC 5987 filename* fallback for names with
// non-ASCII or otherwise unsafe characters) from mime.FormatMediaType
// instead of hand-splicing the name between quotes - a name containing a
// `"` or `\` would otherwise break out of the quoted string and let its
// bytes be interpreted as additional header parameters.
func SetAttachment(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
}
