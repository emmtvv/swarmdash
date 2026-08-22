package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

var FuncMap = template.FuncMap{
	"shortID": func(id string) string {
		if len(id) > 12 {
			return id[:12]
		}
		return id
	},
	"ago": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 24*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		default:
			return fmt.Sprintf("%dd ago", int(d.Hours()/24))
		}
	},
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
	"unixAgo": func(sec int64) string {
		if sec == 0 {
			return "-"
		}
		d := time.Since(time.Unix(sec, 0))
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 24*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		default:
			return fmt.Sprintf("%dd ago", int(d.Hours()/24))
		}
	},
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
	// cspNonce is overridden per-request by Renderer.Render (via a cloned
	// template with its own Funcs()) so inline <script> tags can carry the
	// nonce that matches that response's Content-Security-Policy header.
	// The base definition here only exists so templates parse standalone.
	"cspNonce": func() string { return "" },
}
