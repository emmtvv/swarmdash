package admin

import "net/http"

// redirect sends a normal 303 for full-page form posts, or an HX-Redirect
// for htmx-driven requests (htmx doesn't follow ordinary redirects on
// hx-post/hx-delete by default).
func redirect(w http.ResponseWriter, r *http.Request, location string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
