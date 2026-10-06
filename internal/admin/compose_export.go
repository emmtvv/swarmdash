package admin

import (
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"gopkg.in/yaml.v3"

	"swarmdash/internal/compose"
)

// composeExporter converts running services back into a compose file -
// best-effort, since a live ServiceSpec carries more detail than the schema
// round-trips, but complete enough to deploy again as-is: every network,
// secret, config and named volume a service references is declared at the
// top level too (networks the stack itself created as stack networks,
// everything else as external), which is what lets "Edit stack" fall back
// to an export when no compose file was ever stored for a stack.
type composeExporter struct {
	stack string
	nets  map[string]network.Summary // by ID and by name
	file  *compose.File
}

func newComposeExporter(stack string, nets []network.Summary) *composeExporter {
	x := &composeExporter{
		stack: stack,
		nets:  map[string]network.Summary{},
		file:  &compose.File{Version: "3.8", Services: map[string]compose.Service{}},
	}
	for _, n := range nets {
		x.nets[n.ID] = n
		x.nets[n.Name] = n
	}
	return x
}

// exportService converts one service without any network lookup (network
// attachments export under whatever their target is).
func exportService(svc swarm.Service) compose.Service {
	return newComposeExporter("", nil).service(svc)
}

// add exports svc under its in-stack short name.
func (x *composeExporter) add(svc swarm.Service) {
	short := svc.Spec.Name
	if stack := stackName(svc); stack != "" {
		short = strings.TrimPrefix(svc.Spec.Name, stack+"_")
	}
	x.file.Services[short] = x.service(svc)
}

func (x *composeExporter) service(svc swarm.Service) compose.Service {
	out := compose.Service{}
	if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
		x.container(cs, &out)
	}

	ports := svc.Endpoint.Spec.Ports
	if svc.Spec.EndpointSpec != nil {
		ports = svc.Spec.EndpointSpec.Ports
		if svc.Spec.EndpointSpec.Mode == swarm.ResolutionModeDNSRR {
			out.Deploy.EndpointMode = "dnsrr"
		}
	}
	for _, p := range ports {
		port := compose.Port{Target: p.TargetPort, Published: p.PublishedPort}
		if p.Protocol != "" && p.Protocol != swarm.PortConfigProtocolTCP {
			port.Protocol = string(p.Protocol)
		}
		if p.PublishMode == swarm.PortConfigPublishModeHost {
			port.Mode = "host"
		}
		out.Ports = append(out.Ports, port)
	}

	for _, na := range svc.Spec.TaskTemplate.Networks {
		out.Networks = append(out.Networks, compose.ServiceNetwork{Name: x.networkKey(na.Target), Aliases: na.Aliases})
	}

	if ld := svc.Spec.TaskTemplate.LogDriver; ld != nil && ld.Name != "" {
		out.Logging = &compose.Logging{Driver: ld.Name, Options: ld.Options}
	}

	x.deploy(svc.Spec, &out.Deploy)
	return out
}

func (x *composeExporter) container(cs *swarm.ContainerSpec, out *compose.Service) {
	out.Image = cs.Image
	out.Entrypoint = compose.StrList(cs.Command)
	out.Command = compose.StrList(cs.Args)
	if len(cs.Env) > 0 {
		out.Environment = compose.EnvMap{}
		for _, kv := range cs.Env {
			k, v, _ := strings.Cut(kv, "=")
			out.Environment[k] = v
		}
	}
	out.Labels = withoutStackLabel(cs.Labels)
	out.User = cs.User
	out.WorkingDir = cs.Dir
	out.Hostname = cs.Hostname
	out.StopSignal = cs.StopSignal
	if cs.StopGracePeriod != nil {
		out.StopGracePeriod = cs.StopGracePeriod.String()
	}
	out.Init = cs.Init
	out.ReadOnly = cs.ReadOnly
	out.TTY = cs.TTY
	out.CapAdd = cs.CapabilityAdd
	out.CapDrop = cs.CapabilityDrop
	if len(cs.Sysctls) > 0 {
		out.Sysctls = compose.EnvMap(cs.Sysctls)
	}
	if len(cs.Ulimits) > 0 {
		out.Ulimits = map[string]compose.Ulimit{}
		for _, u := range cs.Ulimits {
			out.Ulimits[u.Name] = compose.Ulimit{Soft: u.Soft, Hard: u.Hard}
		}
	}
	if cs.DNSConfig != nil {
		out.DNS = cs.DNSConfig.Nameservers
	}
	for _, h := range cs.Hosts {
		// swarmkit stores /etc/hosts order ("IP host [aliases]").
		if f := strings.Fields(h); len(f) >= 2 {
			for _, host := range f[1:] {
				out.ExtraHosts = append(out.ExtraHosts, host+":"+f[0])
			}
		}
	}
	if hc := cs.Healthcheck; hc != nil {
		out.Healthcheck = &compose.Healthcheck{Test: compose.HealthTest(hc.Test)}
		if len(hc.Test) == 1 && hc.Test[0] == "NONE" {
			out.Healthcheck = &compose.Healthcheck{Disable: true}
		}
		if hc.Interval > 0 {
			out.Healthcheck.Interval = hc.Interval.String()
		}
		if hc.Timeout > 0 {
			out.Healthcheck.Timeout = hc.Timeout.String()
		}
		if hc.StartPeriod > 0 {
			out.Healthcheck.StartPeriod = hc.StartPeriod.String()
		}
		if hc.StartInterval > 0 {
			out.Healthcheck.StartInterval = hc.StartInterval.String()
		}
		if hc.Retries > 0 {
			retries := hc.Retries
			out.Healthcheck.Retries = &retries
		}
	}

	for _, m := range cs.Mounts {
		out.Volumes = append(out.Volumes, x.mount(m))
	}
	for _, sec := range cs.Secrets {
		out.Secrets = append(out.Secrets, x.fileRef(sec.SecretName, sec.File))
		if x.file.Secrets == nil {
			x.file.Secrets = map[string]compose.ExternalRef{}
		}
		x.file.Secrets[sec.SecretName] = compose.ExternalRef{External: true}
	}
	for _, cfg := range cs.Configs {
		var ft *swarm.SecretReferenceFileTarget
		if cfg.File != nil {
			ft = &swarm.SecretReferenceFileTarget{Name: cfg.File.Name, UID: cfg.File.UID, GID: cfg.File.GID, Mode: cfg.File.Mode}
		}
		out.Configs = append(out.Configs, x.fileRef(cfg.ConfigName, ft))
		if x.file.Configs == nil {
			x.file.Configs = map[string]compose.ConfigDef{}
		}
		x.file.Configs[cfg.ConfigName] = compose.ConfigDef{External: true}
	}
}

// fileRef exports a secret/config attachment, using the long syntax only
// when the target/ownership differ from compose's defaults.
func (x *composeExporter) fileRef(name string, ft *swarm.SecretReferenceFileTarget) compose.FileRef {
	ref := compose.FileRef{Source: name}
	if ft == nil {
		return ref
	}
	if ft.Name != "" && ft.Name != name {
		ref.Target = ft.Name
	}
	if ft.UID != "" && ft.UID != "0" {
		ref.UID = ft.UID
	}
	if ft.GID != "" && ft.GID != "0" {
		ref.GID = ft.GID
	}
	if ft.Mode != 0 && ft.Mode != 0o444 {
		mode := uint32(ft.Mode)
		ref.Mode = &mode
	}
	return ref
}

func (x *composeExporter) mount(m mount.Mount) compose.VolumeMount {
	v := compose.VolumeMount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly}
	switch {
	case m.Type == mount.TypeTmpfs:
		v.Type, v.Source = "tmpfs", ""
		if o := m.TmpfsOptions; o != nil && (o.SizeBytes > 0 || o.Mode != 0) {
			v.Tmpfs = &compose.TmpfsOptions{Mode: uint32(o.Mode)}
			if o.SizeBytes > 0 {
				v.Tmpfs.Size = fmt.Sprintf("%d", o.SizeBytes)
			}
		}
	case m.Type == mount.TypeBind || (m.Type == "" && strings.HasPrefix(m.Source, "/")):
		v.Type = "bind"
		if m.BindOptions != nil && m.BindOptions.Propagation != "" {
			v.Bind = &compose.BindOptions{Propagation: string(m.BindOptions.Propagation)}
		}
	default:
		v.Type = "volume"
		if m.VolumeOptions != nil && m.VolumeOptions.NoCopy {
			v.Volume = &compose.VolumeOpts{NoCopy: true}
		}
		if m.Source != "" {
			def := compose.VolumeDef{}
			if vo := m.VolumeOptions; vo != nil {
				def.Labels = compose.EnvMap(vo.Labels)
				if vo.DriverConfig != nil {
					def.Driver = vo.DriverConfig.Name
					def.DriverOpts = vo.DriverConfig.Options
				}
			}
			if x.file.Volumes == nil {
				x.file.Volumes = map[string]compose.VolumeDef{}
			}
			x.file.Volumes[m.Source] = def
		}
	}
	return v
}

// networkKey maps a service's network attachment (by ID once the service
// exists) to the compose key it's declared under, declaring it at the top
// level as a side effect: networks this stack created (labeled with the
// stack and named "<stack>_<key>") as stack networks, everything else -
// including ingress-style shared networks - as external.
func (x *composeExporter) networkKey(target string) string {
	name := target
	var labels map[string]string
	if n, ok := x.nets[target]; ok {
		name, labels = n.Name, n.Labels
	}
	if x.file.Networks == nil {
		x.file.Networks = map[string]compose.NetworkDef{}
	}
	if x.stack != "" && labels[stackLabel] == x.stack && strings.HasPrefix(name, x.stack+"_") {
		key := strings.TrimPrefix(name, x.stack+"_")
		x.file.Networks[key] = compose.NetworkDef{}
		return key
	}
	x.file.Networks[name] = compose.NetworkDef{External: true}
	return name
}

func (x *composeExporter) deploy(spec swarm.ServiceSpec, d *compose.Deploy) {
	if spec.Mode.Global != nil {
		d.Mode = "global"
	} else if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas != nil {
		r := *spec.Mode.Replicated.Replicas
		d.Replicas = &r
	}
	d.Labels = withoutStackLabel(spec.Labels)

	if p := spec.TaskTemplate.Placement; p != nil {
		d.Placement.Constraints = p.Constraints
		d.Placement.MaxReplicasPerNode = p.MaxReplicas
		for _, pref := range p.Preferences {
			if pref.Spread != nil {
				d.Placement.Preferences = append(d.Placement.Preferences, compose.Preference{Spread: pref.Spread.SpreadDescriptor})
			}
		}
	}

	if res := spec.TaskTemplate.Resources; res != nil {
		if l := res.Limits; l != nil && (l.NanoCPUs != 0 || l.MemoryBytes != 0 || l.Pids != 0) {
			d.Resources.Limits = &compose.ResourceSpec{
				CPUs:   cpusToString(l.NanoCPUs),
				Memory: bytesToString(l.MemoryBytes),
				Pids:   l.Pids,
			}
		}
		if r := res.Reservations; r != nil && (r.NanoCPUs != 0 || r.MemoryBytes != 0) {
			d.Resources.Reservations = &compose.ResourceSpec{
				CPUs:   cpusToString(r.NanoCPUs),
				Memory: bytesToString(r.MemoryBytes),
			}
		}
	}

	d.UpdateConfig = exportUpdateConfig(spec.UpdateConfig)
	d.RollbackConfig = exportUpdateConfig(spec.RollbackConfig)
	if rp := spec.TaskTemplate.RestartPolicy; rp != nil {
		d.RestartPolicy = &compose.RestartPolicy{Condition: string(rp.Condition), MaxAttempts: rp.MaxAttempts}
		if rp.Delay != nil {
			d.RestartPolicy.Delay = rp.Delay.String()
		}
		if rp.Window != nil {
			d.RestartPolicy.Window = rp.Window.String()
		}
	}
}

func exportUpdateConfig(uc *swarm.UpdateConfig) *compose.UpdateConfig {
	if uc == nil {
		return nil
	}
	out := &compose.UpdateConfig{
		Parallelism:     int(uc.Parallelism),
		Order:           uc.Order,
		FailureAction:   uc.FailureAction,
		MaxFailureRatio: uc.MaxFailureRatio,
	}
	if uc.Delay > 0 {
		out.Delay = uc.Delay.String()
	}
	if uc.Monitor > 0 {
		out.Monitor = uc.Monitor.String()
	}
	return out
}

func withoutStackLabel(labels map[string]string) compose.EnvMap {
	out := compose.EnvMap{}
	for k, v := range labels {
		if k != stackLabel {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
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

// exportComposeYAML renders a compose file.
func exportComposeYAML(file *compose.File) ([]byte, error) {
	return yaml.Marshal(file)
}
