package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/compose"
)

type serviceDiff struct {
	Name    string
	Action  string // "create", "update", "unchanged"
	Changes []string
}

// previewCompose computes what applyCompose would do without touching
// anything, by building the same target specs and diffing them against
// whatever's currently deployed.
func (s *Server) previewCompose(ctx context.Context, stackName string, file *compose.File) ([]serviceDiff, error) {
	netNameFor, err := s.ensureComposeNetworksDryRun(ctx, stackName, file.Networks)
	if err != nil {
		return nil, err
	}
	secretRefs, err := s.resolveExternalSecrets(ctx, file.Secrets)
	if err != nil {
		return nil, err
	}
	configRefs, err := s.resolveExternalConfigs(ctx, file.Configs)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(file.Services))
	for name := range file.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	var diffs []serviceDiff
	for _, svcName := range names {
		svcDef := file.Services[svcName]
		fullName := stackName + "_" + svcName

		newSpec, err := buildServiceSpec(fullName, stackName, svcDef, netNameFor, secretRefs, configRefs)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svcName, err)
		}

		existing, _, err := s.docker.ServiceInspectWithRaw(ctx, fullName, swarm.ServiceInspectOptions{})
		if err != nil {
			diffs = append(diffs, serviceDiff{Name: svcName, Action: "create"})
			continue
		}

		changes := diffServiceSpec(existing.Spec, newSpec)
		action := "unchanged"
		if len(changes) > 0 {
			action = "update"
		}
		diffs = append(diffs, serviceDiff{Name: svcName, Action: action, Changes: changes})
	}
	return diffs, nil
}

// ensureComposeNetworksDryRun mirrors ensureComposeNetworks's name
// resolution but never creates anything - a preview must not have side
// effects.
func (s *Server) ensureComposeNetworksDryRun(ctx context.Context, stackName string, defs map[string]compose.NetworkDef) (map[string]string, error) {
	out := map[string]string{}
	for key, def := range defs {
		if def.External {
			name := def.Name
			if name == "" {
				name = key
			}
			out[key] = name
			continue
		}
		out[key] = stackName + "_" + key
	}
	return out, nil
}

func diffServiceSpec(old, new swarm.ServiceSpec) []string {
	var changes []string

	oldImage := ""
	if old.TaskTemplate.ContainerSpec != nil {
		oldImage = old.TaskTemplate.ContainerSpec.Image
	}
	newImage := ""
	if new.TaskTemplate.ContainerSpec != nil {
		newImage = new.TaskTemplate.ContainerSpec.Image
	}
	// The running service's image is digest-pinned by the daemon
	// (QueryRegistry resolves "nginx:alpine" to "nginx:alpine@sha256:...")
	// once it's actually applied, but the freshly-built target spec here
	// never is - that resolution only happens inside ServiceUpdate/Create
	// itself. Compare on the tag as written so an unpinned compose image
	// that already matches doesn't show up as a spurious change.
	if imageTag(oldImage) != imageTag(newImage) {
		changes = append(changes, fmt.Sprintf("image: %s -> %s", oldImage, newImage))
	}

	if oldEnv, newEnv := sortedEnv(old), sortedEnv(new); oldEnv != newEnv {
		changes = append(changes, "environment changed")
	}

	oldReplicas, newReplicas := replicasOf(old), replicasOf(new)
	if oldReplicas != newReplicas {
		changes = append(changes, fmt.Sprintf("replicas: %d -> %d", oldReplicas, newReplicas))
	}

	oldMounts := len(mountsOf(old))
	newMounts := len(mountsOf(new))
	if oldMounts != newMounts {
		changes = append(changes, fmt.Sprintf("mounts: %d -> %d", oldMounts, newMounts))
	}

	oldRes := resourceSummary(old)
	newRes := resourceSummary(new)
	if oldRes != newRes {
		changes = append(changes, fmt.Sprintf("resources: %s -> %s", orDefault(oldRes, "none"), orDefault(newRes, "none")))
	}

	return changes
}

func sortedEnv(spec swarm.ServiceSpec) string {
	if spec.TaskTemplate.ContainerSpec == nil {
		return ""
	}
	env := append([]string{}, spec.TaskTemplate.ContainerSpec.Env...)
	sort.Strings(env)
	return strings.Join(env, ",")
}

func replicasOf(spec swarm.ServiceSpec) uint64 {
	if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas != nil {
		return *spec.Mode.Replicated.Replicas
	}
	return 0
}

func mountsOf(spec swarm.ServiceSpec) []string {
	if spec.TaskTemplate.ContainerSpec == nil {
		return nil
	}
	var out []string
	for _, m := range spec.TaskTemplate.ContainerSpec.Mounts {
		out = append(out, m.Source+":"+m.Target)
	}
	return out
}

func resourceSummary(spec swarm.ServiceSpec) string {
	if spec.TaskTemplate.Resources == nil || spec.TaskTemplate.Resources.Limits == nil {
		return ""
	}
	l := spec.TaskTemplate.Resources.Limits
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
