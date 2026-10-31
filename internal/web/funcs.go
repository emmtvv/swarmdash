package web

import (
	"fmt"
	"html/template"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/i18n"
)

// NodeAddr returns a node's reachable IP. Docker sometimes reports a
// manager's Status.Addr as "0.0.0.0" (notably the leader, when the swarm was
// initialised without an explicit --advertise-addr); in that case the real
// address is only available in ManagerStatus.Addr ("ip:2377").
func NodeAddr(n swarm.Node) string {
	addr := n.Status.Addr
	if (addr == "" || addr == "0.0.0.0") && n.ManagerStatus != nil && n.ManagerStatus.Addr != "" {
		if host, _, err := net.SplitHostPort(n.ManagerStatus.Addr); err == nil && host != "" {
			return host
		}
	}
	return addr
}

var FuncMap = template.FuncMap{
	"nodeAddr": NodeAddr,
	// dict builds a map from alternating key/value arguments, for passing
	// more than one value to a {{template}} call.
	"dict": func(kv ...any) (map[string]any, error) {
		if len(kv)%2 != 0 {
			return nil, fmt.Errorf("dict: odd number of arguments")
		}
		m := make(map[string]any, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			k, ok := kv[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
			}
			m[k] = kv[i+1]
		}
		return m, nil
	},
	"shortID": func(id string) string {
		if len(id) > 12 {
			return id[:12]
		}
		return id
	},
	"ago": func(t time.Time) string { return relativeTime(i18n.Default, t) },
	"cpus": func(nanoCPUs int64) string {
		return fmt.Sprintf("%.1f", float64(nanoCPUs)/1e9)
	},
	"cores": func(f float64) string {
		return fmt.Sprintf("%.2f", f)
	},
	"bytesHuman": func(b int64) string {
		const unit = 1024
		if b < unit {
			return fmt.Sprintf("%d B", b)
		}
		div, exp := int64(unit), 0
		for n := b / unit; n >= unit; n /= unit {
			div *= unit
			exp++
		}
		return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
	},
	"unixAgo": func(sec int64) string { return unixRelative(i18n.Default, sec) },
	"round": func(f float64) int64 {
		return int64(f + 0.5)
	},
	"levelClass": func(pct float64) string {
		switch {
		case pct >= 85:
			return "crit"
		case pct >= 60:
			return "warn"
		default:
			return "ok"
		}
	},
	"imageName": func(s string) string {
		if i := strings.Index(s, "@"); i != -1 {
			return s[:i]
		}
		return s
	},
	"isFavorite": func(favs []string, kind, name string) bool {
		key := kind + ":" + name
		for _, f := range favs {
			if f == key {
				return true
			}
		}
		return false
	},
	"upper": strings.ToUpper,
	"initial": func(s string) string {
		if s == "" {
			return "?"
		}
		return strings.ToUpper(string([]rune(s)[:1]))
	},
	"title": func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	},
	// t/tHTML/lang/jsCatalog/ago/unixAgo are overridden per-request by
	// LangFuncs; these base definitions only exist so templates parse
	// standalone.
	"t":     func(msg string, args ...any) string { return i18n.T(i18n.Default, msg, args...) },
	"tHTML": func(msg string, args ...any) template.HTML { return tHTML(i18n.Default, msg, args...) },
	"tn": func(one, other string, n int, args ...any) string {
		return i18n.N(i18n.Default, one, other, n, args...)
	},
	"lang":      func() string { return i18n.Default },
	"jsCatalog": func() map[string]string { return nil },
	"languages": func() []i18n.Language { return i18n.Languages },
	// cspNonce is overridden per-request by Renderer.Render (via a cloned
	// template with its own Funcs()) so inline <script> tags can carry the
	// nonce that matches that response's Content-Security-Policy header.
	// The base definition here only exists so templates parse standalone.
	"cspNonce": func() string { return "" },
}

// LangFuncs returns the template funcs bound to one UI language, for
// Renderer to layer over FuncMap per request.
func LangFuncs(lang string) template.FuncMap {
	return template.FuncMap{
		"t":         func(msg string, args ...any) string { return i18n.T(lang, msg, args...) },
		"tHTML":     func(msg string, args ...any) template.HTML { return tHTML(lang, msg, args...) },
		"tn":        func(one, other string, n int, args ...any) string { return i18n.N(lang, one, other, n, args...) },
		"lang":      func() string { return lang },
		"jsCatalog": func() map[string]string { return i18n.Catalog(lang, JSMessages) },
		"ago":       func(t time.Time) string { return relativeTime(lang, t) },
		"unixAgo":   func(sec int64) string { return unixRelative(lang, sec) },
	}
}

// tHTML translates a message whose English key itself contains markup
// (help paragraphs with inline <strong>/<span class="mono">), returning it
// unescaped. Safe because both the key and every translation are embedded
// in the binary at build time - never user input - while args, which can
// be, are escaped before being formatted in.
func tHTML(lang, msg string, args ...any) template.HTML {
	escaped := make([]any, len(args))
	for i, a := range args {
		escaped[i] = template.HTMLEscapeString(fmt.Sprint(a))
	}
	return template.HTML(i18n.T(lang, msg, escaped...)) //nolint:gosec // see doc comment
}

func relativeTime(lang string, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T(lang, "just now")
	case d < time.Hour:
		return i18n.T(lang, "%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.T(lang, "%dh ago", int(d.Hours()))
	default:
		return i18n.T(lang, "%dd ago", int(d.Hours()/24))
	}
}

func unixRelative(lang string, sec int64) string {
	if sec == 0 {
		return "-"
	}
	return relativeTime(lang, time.Unix(sec, 0))
}

// JSMessages lists the strings core.js/app.js and inline page scripts pass
// to t(), so layout.html can hand the current language's translations of
// exactly these to the browser as a JSON catalog (read by t in core.js).
// TestJSMessagesCoverScripts keeps it in sync with the scripts.
var JSMessages = []string{
	"Not enough history yet - check back in a few minutes.",
	"Running",
	"Desired",
	"CPU",
	"Memory",
	"Download",
	"Now",
	"Console",
	"Logs",
	"%d selected",
	"Click a node for details.",
	"Backup uploaded",
	"Upload failed:",
	"network error",
	"New password for %s:",
	"Click to sort",
	"Searching...",
	"No results.",
	"No matching pages.",
	"Remove from favorites",
	"Add to favorites",
	"Copied",
	"KEY",
	"value",
	"name",
	"target filename (optional)",
	"/host/path or volume name",
	"/container/path",
	"Read-only",
	"Remove",
	"Add",
	// Passed to t() as a variable (the websocket status badges).
	"connected",
	"reconnecting",
	"done",
}
