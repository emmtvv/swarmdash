package admin

import (
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/swarm"
	"gopkg.in/yaml.v3"

	"swarmdash/internal/compose"
)

// exportService converts a running service back into the compose subset
// schema - best-effort, since a live ServiceSpec carries more detail than
// the schema round-trips (e.g. exact mount propagation options collapse to
// the short "src:dst[:ro]" form). Good enough to re-apply or as a starting
// point to edit.
func exportService(svc swarm.Service) compose.Service {
	cs := svc.Spec.TaskTemplate.ContainerSpec
	out := compose.Service{}
	if cs != nil {
		out.Image = cs.Image
		out.Command = compose.StrList(cs.Command)
		if len(cs.Env) > 0 {
			out.Environment = compose.EnvMap{}
			for _, kv := range cs.Env {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) == 2 {
					out.Environment[parts[0]] = parts[1]
				}
			}
		}
		if len(cs.Labels) > 0 {
			out.Labels = compose.EnvMap(cs.Labels)
		}
		for _, m := range cs.Mounts {
			entry := m.Source + ":" + m.Target
			if m.ReadOnly {
				entry += ":ro"
			}
			out.Volumes = append(out.Volumes, entry)
		}
		for _, sec := range cs.Secrets {
			out.Secrets = append(out.Secrets, sec.SecretName)
		}
		for _, cfg := range cs.Configs {
			out.Configs = append(out.Configs, cfg.ConfigName)
		}
	}

	if svc.Endpoint.Spec.Ports != nil {
		for _, p := range svc.Endpoint.Spec.Ports {
			proto := ""
			if p.Protocol != "" && p.Protocol != "tcp" {
				proto = "/" + string(p.Protocol)
			}
			out.Ports = append(out.Ports, fmt.Sprintf("%d:%d%s", p.PublishedPort, p.TargetPort, proto))
		}
	}

	for _, na := range svc.Spec.TaskTemplate.Networks {
		out.Networks = append(out.Networks, na.Target)
	}

	if svc.Spec.Mode.Global != nil {
		out.Deploy.Mode = "global"
	} else if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
		r := *svc.Spec.Mode.Replicated.Replicas
		out.Deploy.Replicas = &r
	}

	if p := svc.Spec.TaskTemplate.Placement; p != nil {
		out.Deploy.Placement.Constraints = p.Constraints
		for _, pref := range p.Preferences {
			if pref.Spread != nil {
				out.Deploy.Placement.Preferences = append(out.Deploy.Placement.Preferences, compose.Preference{Spread: pref.Spread.SpreadDescriptor})
			}
		}
	}

	if res := svc.Spec.TaskTemplate.Resources; res != nil {
		if res.Limits != nil {
			out.Deploy.Resources.Limits = &compose.ResourceSpec{
				CPUs:   cpusToString(res.Limits.NanoCPUs),
				Memory: bytesToString(res.Limits.MemoryBytes),
			}
		}
		if res.Reservations != nil {
			out.Deploy.Resources.Reservations = &compose.ResourceSpec{
				CPUs:   cpusToString(res.Reservations.NanoCPUs),
				Memory: bytesToString(res.Reservations.MemoryBytes),
			}
		}
	}

	if uc := svc.Spec.UpdateConfig; uc != nil {
		out.Deploy.UpdateConfig = &compose.UpdateConfig{
			Parallelism:   int(uc.Parallelism),
			Delay:         uc.Delay.String(),
			Order:         uc.Order,
			FailureAction: uc.FailureAction,
		}
	}
	if rp := svc.Spec.TaskTemplate.RestartPolicy; rp != nil {
		out.Deploy.RestartPolicy = &compose.RestartPolicy{Condition: string(rp.Condition)}
	}

	return out
}

func cpusToString(nano int64) string {
	if nano == 0 {
		return ""
	}
	return fmt.Sprintf("%g", float64(nano)/1e9)
}

func bytesToString(b int64) string {
	if b == 0 {
		return ""
	}
	return fmt.Sprintf("%d", b)
}

// exportComposeYAML renders one or more services (keyed by their short,
// in-stack name) as a compose file.
func exportComposeYAML(services map[string]compose.Service) ([]byte, error) {
	file := compose.File{Version: "3.8", Services: services}
	return yaml.Marshal(file)
}
