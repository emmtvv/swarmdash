package admin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/httpx"
)

// Favorites are per-user pinned resources ("kind:name" strings on
// store.User.Favorites): a star toggle on the services/stacks/nodes pages,
// a Favorites section on the dashboard, and the first group in the command
// palette. Purely a navigation aid - favoriting grants nothing and is
// allowed for viewers too.

const maxFavorites = 50

var favoriteKinds = map[string]bool{"service": true, "stack": true, "node": true}

// favoriteKey is how a favorite is stored and compared.
func favoriteKey(kind, name string) string {
	return kind + ":" + name
}

// handleFavoriteToggle adds or removes one favorite. Answers JSON for the
// star button's fetch() (app.js), or redirects back for a plain form post.
func (s *Server) handleFavoriteToggle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	kind, name := r.FormValue("kind"), strings.TrimSpace(r.FormValue("name"))
	if !favoriteKinds[kind] || name == "" || len(name) > 256 {
		http.Error(w, "bad favorite", http.StatusBadRequest)
		return
	}
	// API-token users are synthetic (see userFromAPIToken) and have no
	// stored record to keep favorites on.
	user, err := s.store.GetUser(userFromContext(r).Username)
	if err != nil {
		http.Error(w, "favorites are only available to user accounts", http.StatusBadRequest)
		return
	}

	key := favoriteKey(kind, name)
	on := !slices.Contains(user.Favorites, key)
	if on {
		if len(user.Favorites) >= maxFavorites {
			http.Error(w, s.t(r, "you can have at most %d favorites", maxFavorites), http.StatusBadRequest)
			return
		}
		user.Favorites = append(slices.Clone(user.Favorites), key)
	} else {
		user.Favorites = slices.DeleteFunc(slices.Clone(user.Favorites), func(f string) bool { return f == key })
	}
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("Accept") == "application/json" {
		httpx.WriteJSON(w, map[string]any{"favorite": on})
		return
	}
	redirect(w, r, safeReturnPath(r.FormValue("return")))
}

// favoriteItem is one resolved favorite for the dashboard.
type favoriteItem struct {
	Kind   string
	Name   string
	Label  string
	Href   string
	Status string // "ok", "warn", "crit" or "gone"
	Detail string
}

// resolveFavorites turns a user's favorite keys into display rows with live
// status, in the order they were added. A favorite whose target no longer
// exists is kept (status "gone") so the user can see and remove it rather
// than having it silently vanish.
func resolveFavorites(keys []string, services []swarm.Service, nodes []swarm.Node) []favoriteItem {
	svcByName := make(map[string]swarm.Service, len(services))
	stacks := map[string][]swarm.Service{}
	for _, svc := range services {
		svcByName[svc.Spec.Name] = svc
		if st := stackName(svc); st != "" {
			stacks[st] = append(stacks[st], svc)
		}
	}
	nodeByID := make(map[string]swarm.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}

	out := make([]favoriteItem, 0, len(keys))
	for _, key := range keys {
		kind, name, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		it := favoriteItem{Kind: kind, Name: name, Label: name, Status: "gone"}
		switch kind {
		case "service":
			it.Href = "/services/" + name
			if svc, ok := svcByName[name]; ok {
				it.Status, it.Detail = serviceHealth(svc)
			}
		case "stack":
			it.Href = "/stacks/" + name
			if svcs, ok := stacks[name]; ok {
				degraded := 0
				for _, svc := range svcs {
					if st, _ := serviceHealth(svc); st != "ok" {
						degraded++
					}
				}
				it.Status = "ok"
				if degraded > 0 {
					it.Status = "warn"
				}
				it.Detail = formatCount(len(svcs)-degraded, len(svcs))
			}
		case "node":
			it.Href = "/nodes/" + name
			if n, ok := nodeByID[name]; ok {
				it.Label = n.Description.Hostname
				it.Detail = string(n.Status.State)
				switch {
				case n.Status.State != swarm.NodeStateReady:
					it.Status = "crit"
				case n.Spec.Availability != swarm.NodeAvailabilityActive:
					it.Status = "warn"
					it.Detail = string(n.Spec.Availability)
				default:
					it.Status = "ok"
				}
			}
		default:
			continue
		}
		out = append(out, it)
	}
	return out
}

// serviceHealth summarizes a service's running vs. desired tasks.
func serviceHealth(svc swarm.Service) (status, detail string) {
	if svc.ServiceStatus == nil {
		return "ok", ""
	}
	run, want := int(svc.ServiceStatus.RunningTasks), int(svc.ServiceStatus.DesiredTasks)
	switch {
	case run == 0 && want > 0:
		status = "crit"
	case run < want:
		status = "warn"
	default:
		status = "ok"
	}
	return status, formatCount(run, want)
}

func formatCount(a, b int) string {
	return strconv.Itoa(a) + "/" + strconv.Itoa(b)
}
