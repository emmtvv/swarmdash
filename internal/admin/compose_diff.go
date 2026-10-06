package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/compose"
)

type serviceDiff struct {
	Name    string
	Action  string // "create", "update", "unchanged", or "orphan" (in the stack, not in the file)
	Changes []string
}

// previewCompose computes what applyCompose would do without touching
// anything, by building the same target specs and diffing them against
// whatever's currently deployed. Services the stack has but the file no
// longer defines come back as "orphan" - removed only if the deploy is
// submitted with prune.
func (s *Server) previewCompose(ctx context.Context, stackName string, file *compose.File) ([]serviceDiff, error) {
	env, err := s.resolveCompose(ctx, stackName, file, true)
	if err != nil {
		return nil, err
	}

	// A created service's network attachments name networks by ID; map
	// them back so they compare against the names the file resolves to.
	netNames := map[string]string{}
	if nets, err := s.docker.NetworkList(ctx, network.ListOptions{}); err == nil {
		for _, n := range nets {
			netNames[n.ID] = n.Name
		}
	}

	var diffs []serviceDiff
	for _, svcName := range sortedServiceNames(file) {
		fullName := stackName + "_" + svcName

		newSpec, err := buildServiceSpec(fullName, file.Services[svcName], env)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svcName, err)
		}

		existing, _, err := s.docker.ServiceInspectWithRaw(ctx, fullName, swarm.ServiceInspectOptions{})
		if err != nil {
			diffs = append(diffs, serviceDiff{Name: svcName, Action: "create"})
			continue
		}

		changes := diffServiceSpec(existing.Spec, newSpec, netNames)
		action := "unchanged"
		if len(changes) > 0 {
			action = "update"
		}
		diffs = append(diffs, serviceDiff{Name: svcName, Action: action, Changes: changes})
	}

	orphans, err := s.orphanedStackServices(ctx, stackName, file)
	if err != nil {
		return nil, err
	}
	for _, svc := range orphans {
		diffs = append(diffs, serviceDiff{
			Name:    strings.TrimPrefix(svc.Spec.Name, stackName+"_"),
			Action:  "orphan",
			Changes: []string{"no longer in the file - removed only with \"Remove services not in the file\""},
		})
	}
	return diffs, nil
}

// diffServiceSpec lists human-readable differences between a running
// service's spec and a freshly-built target spec. It deliberately compares
// normalized summaries rather than the raw structs: the daemon fills in
// defaults (digest-pinned images, network IDs, platform placement, ...)
// that would otherwise show up as spurious changes on every preview.
func diffServiceSpec(old, new swarm.ServiceSpec, netNames map[string]string) []string {
	var changes []string
	add := func(label, o, n string) {
		if o == n {
			return
		}
		if len(o) > 60 || len(n) > 60 {
			changes = append(changes, label+" changed")
			return
		}
		changes = append(changes, fmt.Sprintf("%s: %s -> %s", label, orDefault(o, "none"), orDefault(n, "none")))
	}

	oc, nc := containerSpecOf(old), containerSpecOf(new)
	// The running service's image is digest-pinned by the daemon
	// (QueryRegistry resolves "nginx:alpine" to "nginx:alpine@sha256:...")
	// once it's actually applied, but the freshly-built target spec here
	// never is - that resolution only happens inside ServiceUpdate/Create
	// itself. Compare on the tag as written so an unpinned compose image
	// that already matches doesn't show up as a spurious change.
	add("image", imageTag(oc.Image), imageTag(nc.Image))
	add("entrypoint", strings.Join(oc.Command, " "), strings.Join(nc.Command, " "))
	add("command", strings.Join(oc.Args, " "), strings.Join(nc.Args, " "))
	if sortedJoin(oc.Env) != sortedJoin(nc.Env) {
		changes = append(changes, "environment changed")
	}
	if joinKVSorted(oc.Labels) != joinKVSorted(nc.Labels) {
		changes = append(changes, "container labels changed")
	}
	if joinKVSorted(old.Labels) != joinKVSorted(new.Labels) {
		changes = append(changes, "service labels changed")
	}
	add("user", oc.User, nc.User)
	add("working_dir", oc.Dir, nc.Dir)
	add("replicas", modeSummary(old), modeSummary(new))
	add("mounts", sortedJoin(mountsOf(old)), sortedJoin(mountsOf(new)))
	add("ports", portsSummary(old), portsSummary(new))
	add("networks", sortedJoin(networksOf(old, netNames)), sortedJoin(networksOf(new, netNames)))
	add("secrets", sortedJoin(secretsOf(oc)), sortedJoin(secretsOf(nc)))
	add("configs", sortedJoin(configsOf(oc)), sortedJoin(configsOf(nc)))
	add("healthcheck", healthSummary(oc), healthSummary(nc))
	add("resources", resourceSummary(old), resourceSummary(new))
	add("constraints", sortedJoin(constraintsOf(old)), sortedJoin(constraintsOf(new)))
	return changes
}

func containerSpecOf(spec swarm.ServiceSpec) swarm.ContainerSpec {
	if spec.TaskTemplate.ContainerSpec == nil {
		return swarm.ContainerSpec{}
	}
	return *spec.TaskTemplate.ContainerSpec
}

func sortedJoin(list []string) string {
	cp := append([]string{}, list...)
	sort.Strings(cp)
	return strings.Join(cp, ", ")
}

func joinKVSorted(m map[string]string) string {
	var out []string
	for _, k := range sortedKeys(m) {
		out = append(out, k+"="+m[k])
	}
	return strings.Join(out, ",")
}

func modeSummary(spec swarm.ServiceSpec) string {
	if spec.Mode.Global != nil {
		return "global"
	}
	return fmt.Sprintf("%d", replicasOf(spec))
}

func replicasOf(spec swarm.ServiceSpec) uint64 {
	if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas != nil {
		return *spec.Mode.Replicated.Replicas
	}
	return 0
}

func mountsOf(spec swarm.ServiceSpec) []string {
	var out []string
	for _, m := range containerSpecOf(spec).Mounts {
		entry := string(m.Type) + ":" + m.Source + ":" + m.Target
		if m.ReadOnly {
			entry += ":ro"
		}
		out = append(out, entry)
	}
	return out
}

func portsSummary(spec swarm.ServiceSpec) string {
	if spec.EndpointSpec == nil {
		return ""
	}
	var out []string
	for _, p := range spec.EndpointSpec.Ports {
		entry := fmt.Sprintf("%d:%d/%s", p.PublishedPort, p.TargetPort, orDefault(string(p.Protocol), "tcp"))
		if p.PublishMode == swarm.PortConfigPublishModeHost {
			entry += "@host"
		}
		out = append(out, entry)
	}
	return sortedJoin(out)
}

func networksOf(spec swarm.ServiceSpec, netNames map[string]string) []string {
	var out []string
	for _, na := range spec.TaskTemplate.Networks {
		name := na.Target
		if n, ok := netNames[name]; ok {
			name = n
		}
		out = append(out, name)
	}
	return out
}

func secretsOf(cs swarm.ContainerSpec) []string {
	var out []string
	for _, sr := range cs.Secrets {
		target := ""
		if sr.File != nil {
			target = sr.File.Name
		}
		out = append(out, sr.SecretName+"->"+target)
	}
	return out
}

func configsOf(cs swarm.ContainerSpec) []string {
	var out []string
	for _, cr := range cs.Configs {
		target := ""
		if cr.File != nil {
			target = cr.File.Name
		}
		out = append(out, cr.ConfigName+"->"+target)
	}
	return out
}

func healthSummary(cs swarm.ContainerSpec) string {
	hc := cs.Healthcheck
	if hc == nil {
		return ""
	}
	return fmt.Sprintf("%s every %s", strings.Join(hc.Test, " "), hc.Interval)
}

func constraintsOf(spec swarm.ServiceSpec) []string {
	if spec.TaskTemplate.Placement == nil {
		return nil
	}
	return spec.TaskTemplate.Placement.Constraints
}

func resourceSummary(spec swarm.ServiceSpec) string {
	if spec.TaskTemplate.Resources == nil || spec.TaskTemplate.Resources.Limits == nil {
		return ""
	}
	l := spec.TaskTemplate.Resources.Limits
	if l.NanoCPUs == 0 && l.MemoryBytes == 0 {
		return ""
	}
	return fmt.Sprintf("cpu=%s mem=%s", cpusToString(l.NanoCPUs), bytesToString(l.MemoryBytes))
}

// imageTag strips a "@sha256:..." digest suffix, if present, so a
// digest-pinned running image can be compared against an unpinned compose
// reference for the same tag.
func imageTag(image string) string {
	if i := strings.Index(image, "@"); i != -1 {
		return image[:i]
	}
	return image
}
