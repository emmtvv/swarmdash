package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
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
	Removed []string
}

// applyOptions tweaks applyCompose. Prune removes services that carry the
// stack's label but are no longer defined in the file (what `docker stack
// deploy --prune` does) - off by default, matching docker's own default,
// so a partial compose file can never delete services by omission unless
// the operator asked for it.
type applyOptions struct {
	Prune bool
}

// resolvedRef is a secret or config a compose key resolved to.
type resolvedRef struct {
	ID   string
	Name string
}

// composeEnv is everything a compose file's services reference outside
// themselves, resolved to real swarm objects: network keys to network
// names, secret/config keys to IDs, volume keys to their top-level
// definitions.
type composeEnv struct {
	stack    string
	networks map[string]string
	secrets  map[string]resolvedRef
	configs  map[string]resolvedRef
	volumes  map[string]compose.VolumeDef
}

// applyCompose creates/updates every swarm resource a compose file
// describes, under the given stack name - a simplified version of what
// `docker stack deploy` does. See internal/compose for the schema subset
// supported.
func (s *Server) applyCompose(ctx context.Context, stackName string, file *compose.File, opts applyOptions) (applyResult, error) {
	var result applyResult

	env, err := s.resolveCompose(ctx, stackName, file, false)
	if err != nil {
		return result, err
	}

	for _, svcName := range sortedServiceNames(file) {
		svcDef := file.Services[svcName]
		fullName := stackName + "_" + svcName

		spec, err := buildServiceSpec(fullName, svcDef, env)
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

	if opts.Prune {
		orphans, err := s.orphanedStackServices(ctx, stackName, file)
		if err != nil {
			return result, err
		}
		for _, svc := range orphans {
			if err := s.docker.ServiceRemove(ctx, svc.ID); err != nil {
				return result, fmt.Errorf("remove service %q: %w", svc.Spec.Name, err)
			}
			result.Removed = append(result.Removed, svc.Spec.Name)
		}
	}

	return result, nil
}

// orphanedStackServices returns the services labeled as part of stackName
// that file no longer defines.
func (s *Server) orphanedStackServices(ctx context.Context, stack string, file *compose.File) ([]swarm.Service, error) {
	services, err := s.listServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	var out []swarm.Service
	for _, svc := range services {
		if stackName(svc) != stack {
			continue
		}
		short := strings.TrimPrefix(svc.Spec.Name, stack+"_")
		if _, ok := file.Services[short]; !ok {
			out = append(out, svc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
	return out, nil
}

func sortedServiceNames(file *compose.File) []string {
	names := make([]string, 0, len(file.Services))
	for name := range file.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// resolveCompose resolves everything file's services reference. With
// dryRun it never creates anything (stack networks and inline configs are
// resolved to the names they *would* get) - a preview must not have side
// effects.
func (s *Server) resolveCompose(ctx context.Context, stackName string, file *compose.File, dryRun bool) (composeEnv, error) {
	env := composeEnv{stack: stackName, volumes: file.Volumes}
	var err error
	if env.networks, err = s.ensureComposeNetworks(ctx, stackName, file.Networks, dryRun); err != nil {
		return env, err
	}
	if env.secrets, err = s.resolveExternalSecrets(ctx, file.Secrets); err != nil {
		return env, err
	}
	if env.configs, err = s.ensureComposeConfigs(ctx, stackName, file.Configs, dryRun); err != nil {
		return env, err
	}
	return env, nil
}

// ensureComposeNetworks creates any non-external overlay network the
// compose file declares (prefixed with the stack name, matching `docker
// stack deploy` naming) and returns a lookup from the compose-file network
// key to the real network name to attach services to.
func (s *Server) ensureComposeNetworks(ctx context.Context, stackName string, defs map[string]compose.NetworkDef, dryRun bool) (map[string]string, error) {
	out := map[string]string{}
	for key, def := range defs {
		if def.External {
			out[key] = orDefault(def.Name, key)
			continue
		}
		name := orDefault(def.Name, stackName+"_"+key)
		out[key] = name
		if dryRun {
			continue
		}
		existing, err := s.docker.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("name", name))})
		if err != nil {
			return nil, fmt.Errorf("list networks: %w", err)
		}
		if hasExactNetwork(existing, name) {
			continue
		}
		labels := map[string]string{stackLabel: stackName}
		for k, v := range def.Labels {
			labels[k] = v
		}
		if _, err := s.docker.NetworkCreate(ctx, name, network.CreateOptions{
			Driver:     orDefault(def.Driver, "overlay"),
			Options:    def.DriverOpts,
			Attachable: def.Attachable,
			Internal:   def.Internal,
			Labels:     labels,
		}); err != nil {
			return nil, fmt.Errorf("create network %q: %w", name, err)
		}
	}
	return out, nil
}

// hasExactNetwork reports whether list (from a NetworkList "name" filter,
// which matches by prefix/substring) contains a network named exactly name.
func hasExactNetwork(list []network.Summary, name string) bool {
	for _, n := range list {
		if n.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) resolveExternalSecrets(ctx context.Context, defs map[string]compose.ExternalRef) (map[string]resolvedRef, error) {
	out := map[string]resolvedRef{}
	for key, ref := range defs {
		name := orDefault(ref.Name, key)
		sec, err := s.findSecretByName(ctx, name)
		if err != nil {
			return nil, err
		}
		out[key] = resolvedRef{ID: sec.ID, Name: sec.Spec.Name}
	}
	return out, nil
}

// ensureComposeConfigs resolves top-level configs: external ones must
// already exist; ones with inline content are created as
// "<stack>_<key>_<hash>" (content-addressed, since a swarm config is
// immutable - changing the content yields a new config name, and the
// service update that switches to it rolls the tasks like any other spec
// change).
func (s *Server) ensureComposeConfigs(ctx context.Context, stackName string, defs map[string]compose.ConfigDef, dryRun bool) (map[string]resolvedRef, error) {
	out := map[string]resolvedRef{}
	for key, def := range defs {
		if def.Content == "" {
			name := orDefault(def.Name, key)
			cfg, err := s.findConfigByName(ctx, name)
			if err != nil {
				return nil, err
			}
			out[key] = resolvedRef{ID: cfg.ID, Name: cfg.Spec.Name}
			continue
		}

		name := inlineConfigName(stackName, key, def.Content)
		cfg, err := s.findConfigByName(ctx, name)
		if err == nil {
			out[key] = resolvedRef{ID: cfg.ID, Name: cfg.Spec.Name}
			continue
		}
		if dryRun {
			out[key] = resolvedRef{Name: name}
			continue
		}
		resp, err := s.docker.ConfigCreate(ctx, swarm.ConfigSpec{
			Annotations: swarm.Annotations{Name: name, Labels: map[string]string{stackLabel: stackName}},
			Data:        []byte(def.Content),
		})
		if err != nil {
			return nil, fmt.Errorf("create config %q: %w", name, err)
		}
		out[key] = resolvedRef{ID: resp.ID, Name: name}
	}
	return out, nil
}

// inlineConfigName is the content-addressed name an inline compose config
// is created under. Swarm caps object names at 64 characters.
func inlineConfigName(stackName, key, content string) string {
	sum := sha256.Sum256([]byte(content))
	suffix := "_" + hex.EncodeToString(sum[:])[:10]
	base := stackName + "_" + key
	if len(base)+len(suffix) > 64 {
		base = base[:64-len(suffix)]
	}
	return base + suffix
}

func (s *Server) findSecretByName(ctx context.Context, name string) (swarm.Secret, error) {
	list, err := s.docker.SecretList(ctx, swarm.SecretListOptions{Filters: filters.NewArgs(filters.Arg("name", name))})
	if err != nil {
		return swarm.Secret{}, fmt.Errorf("look up secret %q: %w", name, err)
	}
	for _, sec := range list {
		if sec.Spec.Name == name {
			return sec, nil
		}
	}
	return swarm.Secret{}, fmt.Errorf("secret %q not found - create it under Secrets first", name)
}

func (s *Server) findConfigByName(ctx context.Context, name string) (swarm.Config, error) {
	list, err := s.docker.ConfigList(ctx, swarm.ConfigListOptions{Filters: filters.NewArgs(filters.Arg("name", name))})
	if err != nil {
		return swarm.Config{}, fmt.Errorf("look up config %q: %w", name, err)
	}
	for _, cfg := range list {
		if cfg.Spec.Name == name {
			return cfg, nil
		}
	}
	return swarm.Config{}, fmt.Errorf("config %q not found - create it under Configs first", name)
}

func buildServiceSpec(fullName string, svc compose.Service, env composeEnv) (swarm.ServiceSpec, error) {
	if svc.Image == "" {
		return swarm.ServiceSpec{}, fmt.Errorf("image is required")
	}
	labels := map[string]string{stackLabel: env.stack}
	for k, v := range svc.Deploy.Labels {
		labels[k] = v
	}

	cs := &swarm.ContainerSpec{
		Image:          svc.Image,
		Command:        svc.Entrypoint,
		Args:           svc.Command,
		Env:            sortedEnvList(svc.Environment),
		Labels:         svc.Labels,
		User:           svc.User,
		Dir:            svc.WorkingDir,
		Hostname:       svc.Hostname,
		StopSignal:     svc.StopSignal,
		Init:           svc.Init,
		ReadOnly:       svc.ReadOnly,
		TTY:            svc.TTY,
		CapabilityAdd:  svc.CapAdd,
		CapabilityDrop: svc.CapDrop,
		Sysctls:        svc.Sysctls,
	}

	var err error
	if cs.StopGracePeriod, err = optDuration("stop_grace_period", svc.StopGracePeriod); err != nil {
		return swarm.ServiceSpec{}, err
	}
	for _, name := range sortedKeys(svc.Ulimits) {
		u := svc.Ulimits[name]
		cs.Ulimits = append(cs.Ulimits, &container.Ulimit{Name: name, Soft: u.Soft, Hard: u.Hard})
	}
	if len(svc.DNS) > 0 {
		cs.DNSConfig = &swarm.DNSConfig{Nameservers: svc.DNS}
	}
	for _, h := range svc.ExtraHosts {
		host, ip, ok := strings.Cut(h, ":")
		if !ok {
			return swarm.ServiceSpec{}, fmt.Errorf("invalid extra_hosts entry %q (want host:ip)", h)
		}
		// swarmkit wants /etc/hosts order: "IP hostname".
		cs.Hosts = append(cs.Hosts, ip+" "+host)
	}
	if cs.Healthcheck, err = composeHealthcheck(svc.Healthcheck); err != nil {
		return swarm.ServiceSpec{}, err
	}
	if cs.Mounts, err = composeMounts(svc.Volumes, env.volumes); err != nil {
		return swarm.ServiceSpec{}, err
	}

	for _, ref := range svc.Secrets {
		r, ok := env.secrets[ref.Source]
		if !ok {
			return swarm.ServiceSpec{}, fmt.Errorf("secret %q referenced but not declared in top-level secrets", ref.Source)
		}
		cs.Secrets = append(cs.Secrets, &swarm.SecretReference{SecretID: r.ID, SecretName: r.Name, File: fileTarget(ref)})
	}
	for _, ref := range svc.Configs {
		r, ok := env.configs[ref.Source]
		if !ok {
			return swarm.ServiceSpec{}, fmt.Errorf("config %q referenced but not declared in top-level configs", ref.Source)
		}
		ft := fileTarget(ref)
		cs.Configs = append(cs.Configs, &swarm.ConfigReference{ConfigID: r.ID, ConfigName: r.Name, File: &swarm.ConfigReferenceFileTarget{
			Name: ft.Name, UID: ft.UID, GID: ft.GID, Mode: ft.Mode,
		}})
	}

	var networks []swarm.NetworkAttachmentConfig
	for _, sn := range svc.Networks {
		name, ok := env.networks[sn.Name]
		if !ok {
			return swarm.ServiceSpec{}, fmt.Errorf("network %q referenced but not declared in top-level networks", sn.Name)
		}
		networks = append(networks, swarm.NetworkAttachmentConfig{Target: name, Aliases: sn.Aliases})
	}

	mode := swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: uint64Ptr(1)}}
	if svc.Deploy.Replicas != nil {
		mode.Replicated.Replicas = svc.Deploy.Replicas
	}
	switch strings.ToLower(svc.Deploy.Mode) {
	case "", "replicated":
	case "global":
		mode = swarm.ServiceMode{Global: &swarm.GlobalService{}}
	default:
		return swarm.ServiceSpec{}, fmt.Errorf("unsupported deploy.mode %q (want replicated or global)", svc.Deploy.Mode)
	}

	resources, err := parseResources(svc.Deploy.Resources)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	restartPolicy, err := composeRestartPolicy(svc.Deploy.RestartPolicy)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	updateConfig, err := composeUpdateConfig("update_config", svc.Deploy.UpdateConfig)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	rollbackConfig, err := composeUpdateConfig("rollback_config", svc.Deploy.RollbackConfig)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}

	var placement *swarm.Placement
	if p := svc.Deploy.Placement; len(p.Constraints) > 0 || len(p.Preferences) > 0 || p.MaxReplicasPerNode > 0 {
		placement = &swarm.Placement{Constraints: p.Constraints, MaxReplicas: p.MaxReplicasPerNode}
		for _, pref := range p.Preferences {
			if pref.Spread == "" {
				continue
			}
			placement.Preferences = append(placement.Preferences, swarm.PlacementPreference{
				Spread: &swarm.SpreadOver{SpreadDescriptor: pref.Spread},
			})
		}
	}

	var logDriver *swarm.Driver
	if svc.Logging != nil && svc.Logging.Driver != "" {
		logDriver = &swarm.Driver{Name: svc.Logging.Driver, Options: svc.Logging.Options}
	}

	endpoint := &swarm.EndpointSpec{Ports: composePorts(svc.Ports)}
	switch svc.Deploy.EndpointMode {
	case "":
	case "vip", "dnsrr":
		endpoint.Mode = swarm.ResolutionMode(svc.Deploy.EndpointMode)
	default:
		return swarm.ServiceSpec{}, fmt.Errorf("unsupported deploy.endpoint_mode %q (want vip or dnsrr)", svc.Deploy.EndpointMode)
	}

	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: fullName, Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: cs,
			Networks:      networks,
			Resources:     resources,
			RestartPolicy: restartPolicy,
			Placement:     placement,
			LogDriver:     logDriver,
		},
		Mode:           mode,
		UpdateConfig:   updateConfig,
		RollbackConfig: rollbackConfig,
		EndpointSpec:   endpoint,
	}
	return spec, nil
}

// fileTarget builds a secret/config file target from a compose reference.
// Like compose, the default target is the reference's key (so renaming the
// underlying secret via `name:` - e.g. after a rotation - doesn't move the
// file inside the container), owned by root, world-readable.
func fileTarget(ref compose.FileRef) *swarm.SecretReferenceFileTarget {
	ft := &swarm.SecretReferenceFileTarget{
		Name: orDefault(ref.Target, ref.Source),
		UID:  orDefault(ref.UID, "0"),
		GID:  orDefault(ref.GID, "0"),
		Mode: 0o444,
	}
	if ref.Mode != nil {
		ft.Mode = fileModeOf(*ref.Mode)
	}
	return ft
}

func fileModeOf(m uint32) os.FileMode { return os.FileMode(m) }

func sortedEnvList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		out = append(out, k+"="+env[k])
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func optDuration(field, v string) (*time.Duration, error) {
	if v == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", field, v, err)
	}
	return &d, nil
}

func durationOrZero(field, v string) (time.Duration, error) {
	d, err := optDuration(field, v)
	if err != nil || d == nil {
		return 0, err
	}
	return *d, nil
}

func composeHealthcheck(hc *compose.Healthcheck) (*container.HealthConfig, error) {
	if hc == nil {
		return nil, nil
	}
	out := &container.HealthConfig{Test: []string(hc.Test)}
	if hc.Disable {
		out.Test = []string{"NONE"}
	}
	var err error
	if out.Interval, err = durationOrZero("healthcheck.interval", hc.Interval); err != nil {
		return nil, err
	}
	if out.Timeout, err = durationOrZero("healthcheck.timeout", hc.Timeout); err != nil {
		return nil, err
	}
	if out.StartPeriod, err = durationOrZero("healthcheck.start_period", hc.StartPeriod); err != nil {
		return nil, err
	}
	if out.StartInterval, err = durationOrZero("healthcheck.start_interval", hc.StartInterval); err != nil {
		return nil, err
	}
	if hc.Retries != nil {
		out.Retries = *hc.Retries
	}
	return out, nil
}

func composeRestartPolicy(rp *compose.RestartPolicy) (*swarm.RestartPolicy, error) {
	if rp == nil {
		return nil, nil
	}
	out := &swarm.RestartPolicy{
		Condition:   swarm.RestartPolicyCondition(orDefault(rp.Condition, "any")),
		MaxAttempts: rp.MaxAttempts,
	}
	switch out.Condition {
	case swarm.RestartPolicyConditionAny, swarm.RestartPolicyConditionOnFailure, swarm.RestartPolicyConditionNone:
	default:
		return nil, fmt.Errorf("unsupported restart_policy.condition %q (want any, on-failure or none)", rp.Condition)
	}
	var err error
	if out.Delay, err = optDuration("restart_policy.delay", rp.Delay); err != nil {
		return nil, err
	}
	if out.Window, err = optDuration("restart_policy.window", rp.Window); err != nil {
		return nil, err
	}
	return out, nil
}

func composeUpdateConfig(field string, uc *compose.UpdateConfig) (*swarm.UpdateConfig, error) {
	if uc == nil {
		return nil, nil
	}
	delay, err := durationOrZero(field+".delay", uc.Delay)
	if err != nil {
		return nil, err
	}
	monitor, err := durationOrZero(field+".monitor", uc.Monitor)
	if err != nil {
		return nil, err
	}
	return &swarm.UpdateConfig{
		Parallelism:     uint64(uc.Parallelism),
		Delay:           delay,
		Monitor:         monitor,
		MaxFailureRatio: uc.MaxFailureRatio,
		FailureAction:   orDefault(uc.FailureAction, "pause"),
		Order:           orDefault(uc.Order, "stop-first"),
	}, nil
}

// composeMounts converts a service's volumes to swarm mounts. A named
// volume declared under top-level `volumes:` carries its driver and
// driver_opts (e.g. NFS) on every mount, since swarm creates volumes
// lazily per node; an external one is used under its `name:`. Named
// volumes keep the key as their name (no stack prefix) - swarmdash has
// always done this, and prefixing now would orphan existing data.
func composeMounts(vols []compose.VolumeMount, defs map[string]compose.VolumeDef) ([]mount.Mount, error) {
	var out []mount.Mount
	for _, v := range vols {
		m := mount.Mount{Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly}
		switch v.Type {
		case "bind":
			m.Type = mount.TypeBind
			if v.Bind != nil && v.Bind.Propagation != "" {
				m.BindOptions = &mount.BindOptions{Propagation: mount.Propagation(v.Bind.Propagation)}
			}
		case "tmpfs":
			m.Type = mount.TypeTmpfs
			m.Source = ""
			if v.Tmpfs != nil {
				opts := &mount.TmpfsOptions{}
				if v.Tmpfs.Size != "" {
					size, err := units.RAMInBytes(v.Tmpfs.Size)
					if err != nil {
						return nil, fmt.Errorf("invalid tmpfs size %q: %w", v.Tmpfs.Size, err)
					}
					opts.SizeBytes = size
				}
				if v.Tmpfs.Mode != 0 {
					opts.Mode = fileModeOf(v.Tmpfs.Mode)
				}
				m.TmpfsOptions = opts
			}
		default:
			m.Type = mount.TypeVolume
			if v.Volume != nil && v.Volume.NoCopy {
				m.VolumeOptions = &mount.VolumeOptions{NoCopy: true}
			}
			if def, ok := defs[v.Source]; ok && v.Source != "" {
				m.Source = orDefault(def.Name, v.Source)
				if !def.External && (def.Driver != "" || len(def.DriverOpts) > 0 || len(def.Labels) > 0) {
					if m.VolumeOptions == nil {
						m.VolumeOptions = &mount.VolumeOptions{}
					}
					m.VolumeOptions.Labels = def.Labels
					if def.Driver != "" || len(def.DriverOpts) > 0 {
						m.VolumeOptions.DriverConfig = &mount.Driver{Name: orDefault(def.Driver, "local"), Options: def.DriverOpts}
					}
				}
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func composePorts(ports compose.PortList) []swarm.PortConfig {
	var out []swarm.PortConfig
	for _, p := range ports {
		pc := swarm.PortConfig{
			Protocol:      swarm.PortConfigProtocol(orDefault(p.Protocol, "tcp")),
			TargetPort:    p.Target,
			PublishedPort: p.Published,
			PublishMode:   swarm.PortConfigPublishModeIngress,
		}
		if p.Mode == "host" {
			pc.PublishMode = swarm.PortConfigPublishModeHost
		}
		out = append(out, pc)
	}
	return out
}

// parseMounts handles compose's short volume syntax ("target",
// "source:target", "source:target:ro"), one entry per line, for the
// service editor form.
func parseMounts(volumes []string) ([]mount.Mount, error) {
	var vols []compose.VolumeMount
	for _, v := range volumes {
		m, err := compose.ParseVolumeSpec(v)
		if err != nil {
			return nil, err
		}
		vols = append(vols, m)
	}
	return composeMounts(vols, nil)
}

// parsePorts handles compose's short port syntax ("8080:80",
// "8080:80/udp", a bare "80", ranges), one entry per line, for the
// service editor form.
func parsePorts(ports []string) ([]swarm.PortConfig, error) {
	var list compose.PortList
	for _, p := range ports {
		parsed, err := compose.ParsePortSpec(p)
		if err != nil {
			return nil, err
		}
		list = append(list, parsed...)
	}
	return composePorts(list), nil
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
	return &swarm.Limit{NanoCPUs: nanoCPUs, MemoryBytes: memBytes, Pids: r.Pids}, nil
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
