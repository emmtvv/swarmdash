package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	units "github.com/docker/go-units"

	"swarmdash/internal/compose"
)

type applyResult struct {
	Created []string
	Updated []string
}

// applyCompose creates/updates every swarm resource a compose file
// describes, under the given stack name - a simplified version of what
// `docker stack deploy` does. See internal/compose for the schema subset
// supported.
func (s *Server) applyCompose(ctx context.Context, stackName string, file *compose.File) (applyResult, error) {
	var result applyResult

	netNameFor, err := s.ensureComposeNetworks(ctx, stackName, file.Networks)
	if err != nil {
		return result, err
	}
	secretRefs, err := s.resolveExternalSecrets(ctx, file.Secrets)
	if err != nil {
		return result, err
	}
	configRefs, err := s.resolveExternalConfigs(ctx, file.Configs)
	if err != nil {
		return result, err
	}

	// Sort for deterministic apply order (map iteration isn't).
	names := make([]string, 0, len(file.Services))
	for name := range file.Services {
		names = append(names, name)
	}

	for _, svcName := range names {
		svcDef := file.Services[svcName]
		fullName := stackName + "_" + svcName

		spec, err := buildServiceSpec(fullName, stackName, svcDef, netNameFor, secretRefs, configRefs)
		if err != nil {
			return result, fmt.Errorf("service %q: %w", svcName, err)
		}

		auth, err := s.encodedRegistryAuthFor(svcDef.Image)
		if err != nil {
			return result, fmt.Errorf("service %q: registry credential: %w", svcName, err)
		}

		existing, _, err := s.docker.ServiceInspectWithRaw(ctx, fullName, swarm.ServiceInspectOptions{})
		if err == nil {
			if _, err := s.docker.ServiceUpdate(ctx, existing.ID, existing.Version, spec, swarm.ServiceUpdateOptions{
				EncodedRegistryAuth: auth,
				QueryRegistry:       true,
			}); err != nil {
				return result, fmt.Errorf("update service %q: %w", svcName, err)
			}
			result.Updated = append(result.Updated, fullName)
			continue
		}

		if _, err := s.docker.ServiceCreate(ctx, spec, swarm.ServiceCreateOptions{
			EncodedRegistryAuth: auth,
			QueryRegistry:       true,
		}); err != nil {
			return result, fmt.Errorf("create service %q: %w", svcName, err)
		}
		result.Created = append(result.Created, fullName)
	}

	return result, nil
}

// ensureComposeNetworks creates any non-external overlay network the
// compose file declares (prefixed with the stack name, matching `docker
// stack deploy` naming) and returns a lookup from the compose-file network
// key to the real network name to attach services to.
func (s *Server) ensureComposeNetworks(ctx context.Context, stackName string, defs map[string]compose.NetworkDef) (map[string]string, error) {
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
		name := stackName + "_" + key
		existing, err := s.docker.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("name", name))})
		if err != nil {
			return nil, fmt.Errorf("list networks: %w", err)
		}
		if len(existing) == 0 {
			driver := def.Driver
			if driver == "" {
				driver = "overlay"
			}
			if _, err := s.docker.NetworkCreate(ctx, name, network.CreateOptions{
				Driver:     driver,
				Attachable: def.Attachable,
				Labels:     map[string]string{stackLabel: stackName},
			}); err != nil {
				return nil, fmt.Errorf("create network %q: %w", name, err)
			}
		}
		out[key] = name
	}
	return out, nil
}

func (s *Server) resolveExternalSecrets(ctx context.Context, defs map[string]compose.ExternalRef) (map[string]*swarm.SecretReference, error) {
	out := map[string]*swarm.SecretReference{}
	for key, ref := range defs {
		name := ref.Name
		if name == "" {
			name = key
		}
		list, err := s.docker.SecretList(ctx, swarm.SecretListOptions{Filters: filters.NewArgs(filters.Arg("name", name))})
		if err != nil {
			return nil, fmt.Errorf("look up secret %q: %w", name, err)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("secret %q not found - create it under Secrets first", name)
		}
		out[key] = &swarm.SecretReference{
			SecretID:   list[0].ID,
			SecretName: list[0].Spec.Name,
			File: &swarm.SecretReferenceFileTarget{
				Name: name,
				UID:  "0",
				GID:  "0",
				Mode: 0o444,
			},
		}
	}
	return out, nil
}

func (s *Server) resolveExternalConfigs(ctx context.Context, defs map[string]compose.ExternalRef) (map[string]*swarm.ConfigReference, error) {
	out := map[string]*swarm.ConfigReference{}
	for key, ref := range defs {
		name := ref.Name
		if name == "" {
			name = key
		}
		list, err := s.docker.ConfigList(ctx, swarm.ConfigListOptions{Filters: filters.NewArgs(filters.Arg("name", name))})
		if err != nil {
			return nil, fmt.Errorf("look up config %q: %w", name, err)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("config %q not found - create it under Configs first", name)
		}
		out[key] = &swarm.ConfigReference{
			ConfigID:   list[0].ID,
			ConfigName: list[0].Spec.Name,
			File: &swarm.ConfigReferenceFileTarget{
				Name: name,
				UID:  "0",
				GID:  "0",
				Mode: 0o444,
			},
		}
	}
	return out, nil
}

func buildServiceSpec(fullName, stackName string, svc compose.Service, netNameFor map[string]string, secretRefs map[string]*swarm.SecretReference, configRefs map[string]*swarm.ConfigReference) (swarm.ServiceSpec, error) {
	labels := map[string]string{stackLabel: stackName}
	for k, v := range svc.Deploy.Labels {
		labels[k] = v
	}

	env := make([]string, 0, len(svc.Environment))
	for k, v := range svc.Environment {
		env = append(env, k+"="+v)
	}

	mounts, err := parseMounts(svc.Volumes)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}

	var secrets []*swarm.SecretReference
	for _, key := range svc.Secrets {
		ref, ok := secretRefs[key]
		if !ok {
			return swarm.ServiceSpec{}, fmt.Errorf("secret %q referenced but not declared in top-level secrets", key)
		}
		secrets = append(secrets, ref)
	}
	var configs []*swarm.ConfigReference
	for _, key := range svc.Configs {
		ref, ok := configRefs[key]
		if !ok {
			return swarm.ServiceSpec{}, fmt.Errorf("config %q referenced but not declared in top-level configs", key)
		}
		configs = append(configs, ref)
	}

	var networks []swarm.NetworkAttachmentConfig
	for _, key := range svc.Networks {
		name, ok := netNameFor[key]
		if !ok {
			return swarm.ServiceSpec{}, fmt.Errorf("network %q referenced but not declared in top-level networks", key)
		}
		networks = append(networks, swarm.NetworkAttachmentConfig{Target: name})
	}

	ports, err := parsePorts(svc.Ports)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}

	mode := swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: uint64Ptr(1)}}
	if svc.Deploy.Replicas != nil {
		mode.Replicated.Replicas = svc.Deploy.Replicas
	}
	if strings.EqualFold(svc.Deploy.Mode, "global") {
		mode = swarm.ServiceMode{Global: &swarm.GlobalService{}}
	}

	resources, err := parseResources(svc.Deploy.Resources)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}

	var restartPolicy *swarm.RestartPolicy
	if svc.Deploy.RestartPolicy != nil {
		restartPolicy = &swarm.RestartPolicy{
			Condition: swarm.RestartPolicyCondition(orDefault(svc.Deploy.RestartPolicy.Condition, "any")),
		}
	}

	var updateConfig *swarm.UpdateConfig
	if svc.Deploy.UpdateConfig != nil {
		uc := svc.Deploy.UpdateConfig
		delay, _ := time.ParseDuration(orDefault(uc.Delay, "0s"))
		updateConfig = &swarm.UpdateConfig{
			Parallelism:   uint64(uc.Parallelism),
			Delay:         delay,
			FailureAction: orDefault(uc.FailureAction, "pause"),
			Order:         orDefault(uc.Order, "stop-first"),
		}
	}

	var placement *swarm.Placement
	if len(svc.Deploy.Placement.Constraints) > 0 || len(svc.Deploy.Placement.Preferences) > 0 {
		placement = &swarm.Placement{Constraints: svc.Deploy.Placement.Constraints}
		for _, pref := range svc.Deploy.Placement.Preferences {
			if pref.Spread == "" {
				continue
			}
			placement.Preferences = append(placement.Preferences, swarm.PlacementPreference{
				Spread: &swarm.SpreadOver{SpreadDescriptor: pref.Spread},
			})
		}
	}

	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: fullName, Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:   svc.Image,
				Command: svc.Command,
				Env:     env,
				Labels:  svc.Labels,
				Mounts:  mounts,
				Secrets: secrets,
				Configs: configs,
			},
			Networks:      networks,
			Resources:     resources,
			RestartPolicy: restartPolicy,
			Placement:     placement,
		},
		Mode:         mode,
		UpdateConfig: updateConfig,
		EndpointSpec: &swarm.EndpointSpec{Ports: ports},
	}
	return spec, nil
}

// parseMounts handles compose's short volume syntax: "target",
// "source:target", "source:target:ro". A source containing "/" is treated
// as a bind mount; otherwise it's a named volume.
func parseMounts(volumes []string) ([]mount.Mount, error) {
	var out []mount.Mount
	for _, v := range volumes {
		parts := strings.Split(v, ":")
		var m mount.Mount
		switch len(parts) {
		case 1:
			m = mount.Mount{Type: mount.TypeVolume, Target: parts[0]}
		case 2, 3:
			m = mount.Mount{Source: parts[0], Target: parts[1]}
			if strings.HasPrefix(parts[0], "/") || strings.HasPrefix(parts[0], ".") {
				m.Type = mount.TypeBind
			} else {
				m.Type = mount.TypeVolume
			}
			if len(parts) == 3 && strings.Contains(parts[2], "ro") {
				m.ReadOnly = true
			}
		default:
			return nil, fmt.Errorf("invalid volume mapping %q", v)
		}
		out = append(out, m)
	}
	return out, nil
}

// parsePorts handles compose's short port syntax: "8080:80",
// "8080:80/udp", or a bare "80" (published == target).
func parsePorts(ports []string) ([]swarm.PortConfig, error) {
	var out []swarm.PortConfig
	for _, p := range ports {
		proto := "tcp"
		if i := strings.LastIndex(p, "/"); i != -1 {
			proto = p[i+1:]
			p = p[:i]
		}
		var published, target uint64
		var err error
		parts := strings.Split(p, ":")
		switch len(parts) {
		case 1:
			target, err = strconv.ParseUint(parts[0], 10, 32)
			published = target
		case 2:
			published, err = strconv.ParseUint(parts[0], 10, 32)
			if err == nil {
				target, err = strconv.ParseUint(parts[1], 10, 32)
			}
		default:
			return nil, fmt.Errorf("invalid port mapping %q", p)
		}
		if err != nil {
			return nil, fmt.Errorf("invalid port mapping %q: %w", p, err)
		}
		out = append(out, swarm.PortConfig{
			Protocol:      swarm.PortConfigProtocol(proto),
			TargetPort:    uint32(target),
			PublishedPort: uint32(published),
			PublishMode:   swarm.PortConfigPublishModeIngress,
		})
	}
	return out, nil
}

func parseResources(r compose.Resources) (*swarm.ResourceRequirements, error) {
	if r.Limits == nil && r.Reservations == nil {
		return nil, nil
	}
	out := &swarm.ResourceRequirements{}
	if r.Limits != nil {
		lim, err := toResourceLimit(r.Limits)
		if err != nil {
			return nil, err
		}
		out.Limits = lim
	}
	if r.Reservations != nil {
		nanoCPUs, memBytes, err := parseCPUMem(r.Reservations.CPUs, r.Reservations.Memory)
		if err != nil {
			return nil, err
		}
		out.Reservations = &swarm.Resources{NanoCPUs: nanoCPUs, MemoryBytes: memBytes}
	}
	return out, nil
}

func toResourceLimit(r *compose.ResourceSpec) (*swarm.Limit, error) {
	nanoCPUs, memBytes, err := parseCPUMem(r.CPUs, r.Memory)
	if err != nil {
		return nil, err
	}
	return &swarm.Limit{NanoCPUs: nanoCPUs, MemoryBytes: memBytes}, nil
}

func parseCPUMem(cpus, mem string) (nanoCPUs int64, memBytes int64, err error) {
	if cpus != "" {
		f, err := strconv.ParseFloat(cpus, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid cpus %q: %w", cpus, err)
		}
		nanoCPUs = int64(f * 1e9)
	}
	if mem != "" {
		memBytes, err = units.RAMInBytes(mem)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid memory %q: %w", mem, err)
		}
	}
	return nanoCPUs, memBytes, nil
}

func uint64Ptr(v uint64) *uint64 { return &v }

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
