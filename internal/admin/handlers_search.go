package admin

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/httpx"
	"swarmdash/internal/web"
)

// Global search behind the command palette (Cmd/Ctrl+K, app.js): one query
// across the swarm-wide objects - services (by name or image), stacks,
// nodes (hostname, ID, address, labels), networks, secrets, configs and
// tasks (by ID prefix). Volumes and images are left out on purpose: they
// live on each node's own daemon (see handleVolumesPage), so searching
// them would mean fanning out to every agent on every keystroke.

const searchPerGroup = 8

type searchHit struct {
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Href   string `json:"href"`
	// Fav is the favoriteKey this hit can be starred as, if any.
	Fav string `json:"fav,omitempty"`
	// rank orders hits within a group: exact, then prefix, then substring.
	rank int
}

type searchGroup struct {
	Kind  string      `json:"kind"`
	Label string      `json:"label"`
	Hits  []searchHit `json:"hits"`
}

// matchRank scores how well q matches any of fields: 0 exact, 1 prefix,
// 2 substring, -1 no match.
func matchRank(q string, fields ...string) int {
	best := -1
	for _, f := range fields {
		f = strings.ToLower(f)
		switch {
		case f == q:
			return 0
		case strings.HasPrefix(f, q):
			best = 1
		case best == -1 && strings.Contains(f, q):
			best = 2
		}
	}
	return best
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	user := userFromContext(r)

	var groups []searchGroup
	if q == "" {
		// An empty query is the palette opening: show favorites first.
		if favs := s.favoriteHits(ctx, user.Favorites); len(favs) > 0 {
			groups = append(groups, searchGroup{Kind: "favorite", Label: s.t(r, "Favorites"), Hits: favs})
		}
		httpx.WriteJSON(w, map[string]any{"groups": groups})
		return
	}

	var (
		wg       sync.WaitGroup
		services []swarm.Service
		nodes    []swarm.Node
		nets     []network.Summary
		secrets  []swarm.Secret
		configs  []swarm.Config
		tasks    []swarm.Task
	)
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	// Errors just leave a group empty - a palette that shows what it could
	// find beats one that fails outright because, say, configs errored.
	run(func() { services, _ = s.listServices(ctx) })
	run(func() { nodes, _ = s.listNodes(ctx) })
	run(func() {
		nets, _ = s.docker.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("scope", "swarm"))})
	})
	run(func() { secrets, _ = s.docker.SecretList(ctx, swarm.SecretListOptions{}) })
	run(func() { configs, _ = s.docker.ConfigList(ctx, swarm.ConfigListOptions{}) })
	if len(q) >= 4 {
		run(func() { tasks = s.tasksByIDPrefix(ctx, q) })
	}
	wg.Wait()

	var svcHits, stackHits, nodeHits, netHits, secretHits, configHits, taskHits []searchHit
	stacksSeen := map[string]bool{}
	for _, svc := range services {
		image := imageWithoutDigest(svc.Spec.TaskTemplate.ContainerSpec.Image)
		if rk := matchRank(q, svc.Spec.Name, image); rk >= 0 {
			svcHits = append(svcHits, searchHit{Label: svc.Spec.Name, Detail: image, Href: "/services/" + svc.Spec.Name, Fav: favoriteKey("service", svc.Spec.Name), rank: rk})
		}
		if st := stackName(svc); st != "" && !stacksSeen[st] {
			stacksSeen[st] = true
			if rk := matchRank(q, st); rk >= 0 {
				stackHits = append(stackHits, searchHit{Label: st, Href: "/stacks/" + st, Fav: favoriteKey("stack", st), rank: rk})
			}
		}
	}
	for _, n := range nodes {
		fields := []string{n.Description.Hostname, n.ID, web.NodeAddr(n)}
		for k, v := range n.Spec.Labels {
			fields = append(fields, k+"="+v)
		}
		if rk := matchRank(q, fields...); rk >= 0 {
			nodeHits = append(nodeHits, searchHit{Label: n.Description.Hostname, Detail: string(n.Spec.Role) + " · " + web.NodeAddr(n), Href: "/nodes/" + n.ID, Fav: favoriteKey("node", n.ID), rank: rk})
		}
	}
	for _, n := range nets {
		if rk := matchRank(q, n.Name, n.ID); rk >= 0 {
			netHits = append(netHits, searchHit{Label: n.Name, Detail: n.Driver, Href: "/networks", rank: rk})
		}
	}
	for _, sec := range secrets {
		if rk := matchRank(q, sec.Spec.Name); rk >= 0 {
			secretHits = append(secretHits, searchHit{Label: sec.Spec.Name, Href: "/secrets/" + sec.ID, rank: rk})
		}
	}
	for _, c := range configs {
		if rk := matchRank(q, c.Spec.Name); rk >= 0 {
			// A config's own page shows its content and is admin-only (see
			// routes.go); viewers land on the list instead of a 403.
			href := "/configs"
			if user.Role == "admin" {
				href = "/configs/" + c.ID
			}
			configHits = append(configHits, searchHit{Label: c.Spec.Name, Href: href, rank: rk})
		}
	}
	svcNames := make(map[string]string, len(services))
	for _, svc := range services {
		svcNames[svc.ID] = svc.Spec.Name
	}
	for _, t := range tasks {
		label := shortTaskID(t.ID)
		if name := svcNames[t.ServiceID]; name != "" {
			label = name + "." + label
		}
		taskHits = append(taskHits, searchHit{Label: label, Detail: string(t.Status.State), Href: "/logs/" + t.ID, rank: 1})
	}

	for _, g := range []struct {
		kind, label string
		hits        []searchHit
	}{
		{"service", "Services", svcHits},
		{"stack", "Stacks", stackHits},
		{"node", "Nodes", nodeHits},
		{"task", "Tasks", taskHits},
		{"network", "Networks", netHits},
		{"secret", "Secrets", secretHits},
		{"config", "Configs", configHits},
	} {
		if len(g.hits) == 0 {
			continue
		}
		sort.SliceStable(g.hits, func(i, j int) bool {
			if g.hits[i].rank != g.hits[j].rank {
				return g.hits[i].rank < g.hits[j].rank
			}
			return g.hits[i].Label < g.hits[j].Label
		})
		if len(g.hits) > searchPerGroup {
			g.hits = g.hits[:searchPerGroup]
		}
		groups = append(groups, searchGroup{Kind: g.kind, Label: s.t(r, g.label), Hits: g.hits})
	}
	httpx.WriteJSON(w, map[string]any{"groups": groups})
}

// tasksByIDPrefix finds tasks whose ID starts with prefix. Docker's task
// "id" filter already matches by prefix.
func (s *Server) tasksByIDPrefix(ctx context.Context, prefix string) []swarm.Task {
	tasks, err := s.docker.TaskList(ctx, swarm.TaskListOptions{Filters: filters.NewArgs(filters.Arg("id", prefix))})
	if err != nil {
		return nil
	}
	if len(tasks) > searchPerGroup {
		tasks = tasks[:searchPerGroup]
	}
	return tasks
}

// favoriteHits resolves favorites for the palette's opening view.
func (s *Server) favoriteHits(ctx context.Context, keys []string) []searchHit {
	if len(keys) == 0 {
		return nil
	}
	services, _ := s.listServices(ctx)
	nodes, _ := s.listNodes(ctx)
	var out []searchHit
	for _, f := range resolveFavorites(keys, services, nodes) {
		out = append(out, searchHit{Label: f.Label, Detail: f.Kind, Href: f.Href, Fav: favoriteKey(f.Kind, f.Name)})
	}
	return out
}

func shortTaskID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func imageWithoutDigest(image string) string {
	if i := strings.Index(image, "@"); i != -1 {
		return image[:i]
	}
	return image
}
