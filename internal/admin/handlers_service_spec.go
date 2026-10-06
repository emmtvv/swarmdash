package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
)

// handleServiceUpdateSpec applies the "advanced" service editor form: env
// vars, labels, mounts, ports, attached networks/secrets/configs,
// entrypoint/command, healthcheck, resource limits/reservations, placement
// constraints, restart policy and rolling-update policy. Unlike scale/
// image (which tweak one field), this replaces each of these fields
// wholesale with what the form submitted - the form is always pre-filled
// with the service's current values, so an unedited submit is a no-op.
func (s *Server) handleServiceUpdateSpec(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	svc, err := s.getService(ctx, name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}
	if svc.Spec.TaskTemplate.ContainerSpec == nil {
		http.Error(w, "service has no container spec", http.StatusBadRequest)
		return
	}

	if err := s.applyEditorForm(ctx, r, &svc.Spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if _, err := s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{}); err != nil {
		http.Error(w, "update service: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "service.update_spec", svc.Spec.Name, "", nil)
	redirect(w, r, "/services/"+name)
}

// applyEditorForm writes the service editor form's fields (see
// partial_service_fields.html) into spec, which must have a non-nil
// ContainerSpec. Shared by the advanced editor on a service's page and the
// New service form, so both accept exactly the same input.
func (s *Server) applyEditorForm(ctx context.Context, r *http.Request, spec *swarm.ServiceSpec) error {
	cs := spec.TaskTemplate.ContainerSpec
	cs.Env = parseLines(r.FormValue("env"))
	cs.Labels = parseKVLines(r.FormValue("labels"))
	cs.Command = parseLines(r.FormValue("entrypoint"))
	cs.Args = parseLines(r.FormValue("command"))

	mounts, err := parseMounts(parseLines(r.FormValue("mounts")))
	if err != nil {
		return fmt.Errorf("invalid mounts: %w", err)
	}
	cs.Mounts = mounts

	networks, err := s.parseNetworkAttachments(ctx, parseLines(r.FormValue("networks")))
	if err != nil {
		return err
	}
	spec.TaskTemplate.Networks = networks

	ports, err := parsePorts(parseLines(r.FormValue("ports")))
	if err != nil {
		return fmt.Errorf("invalid ports: %w", err)
	}
	if len(ports) > 0 {
		spec.EndpointSpec = &swarm.EndpointSpec{Ports: ports}
	} else {
		spec.EndpointSpec = nil
	}

	if cs.Secrets, err = s.parseSecretRefs(ctx, parseLines(r.FormValue("secrets"))); err != nil {
		return err
	}
	if cs.Configs, err = s.parseConfigRefs(ctx, parseLines(r.FormValue("configs"))); err != nil {
		return err
	}
	if cs.Healthcheck, err = parseHealthcheckForm(r); err != nil {
		return err
	}

	constraints := parseLines(r.FormValue("constraints"))
	preferences := parseLines(r.FormValue("preferences"))
	if len(constraints) > 0 || len(preferences) > 0 {
		p := &swarm.Placement{Constraints: constraints}
		for _, descriptor := range preferences {
			p.Preferences = append(p.Preferences, swarm.PlacementPreference{Spread: &swarm.SpreadOver{SpreadDescriptor: descriptor}})
		}
		spec.TaskTemplate.Placement = p
	} else {
		spec.TaskTemplate.Placement = nil
	}

	if spec.TaskTemplate.Resources, err = parseServiceResourceForm(r); err != nil {
		return err
	}

	if cond := r.FormValue("restart_condition"); cond != "" {
		spec.TaskTemplate.RestartPolicy = &swarm.RestartPolicy{Condition: swarm.RestartPolicyCondition(cond)}
	}

	parallelism, _ := strconv.ParseUint(r.FormValue("update_parallelism"), 10, 64)
	delay, _ := time.ParseDuration(orDefault(r.FormValue("update_delay"), "0s"))
	spec.UpdateConfig = &swarm.UpdateConfig{
		Parallelism:   parallelism,
		Delay:         delay,
		Order:         orDefault(r.FormValue("update_order"), "stop-first"),
		FailureAction: orDefault(r.FormValue("update_failure_action"), "pause"),
	}
	return nil
}

// editorFormFromRequest echoes a submitted editor form back as
// editorFormData, so a failed New service submit re-renders with what the
// user typed instead of an empty form.
func editorFormFromRequest(r *http.Request) editorFormData {
	parallelism, _ := strconv.ParseUint(r.FormValue("update_parallelism"), 10, 64)
	return editorFormData{
		Env:               r.FormValue("env"),
		Labels:            r.FormValue("labels"),
		Mounts:            r.FormValue("mounts"),
		Constraints:       r.FormValue("constraints"),
		Preferences:       r.FormValue("preferences"),
		Networks:          r.FormValue("networks"),
		Ports:             r.FormValue("ports"),
		Secrets:           r.FormValue("secrets"),
		Configs:           r.FormValue("configs"),
		Entrypoint:        r.FormValue("entrypoint"),
		Command:           r.FormValue("command"),
		HealthTest:        r.FormValue("health_test"),
		HealthDisabled:    r.FormValue("health_disabled") == "on",
		HealthInterval:    r.FormValue("health_interval"),
		HealthTimeout:     r.FormValue("health_timeout"),
		HealthStartPeriod: r.FormValue("health_start_period"),
		HealthRetries:     r.FormValue("health_retries"),
		CPULimit:          r.FormValue("cpu_limit"),
		MemLimit:          r.FormValue("mem_limit"),
		CPUReservation:    r.FormValue("cpu_reservation"),
		MemReservation:    r.FormValue("mem_reservation"),
		RestartCondition:  orDefault(r.FormValue("restart_condition"), "any"),
		UpdateParallelism: parallelism,
		UpdateDelay:       r.FormValue("update_delay"),
		UpdateOrder:       orDefault(r.FormValue("update_order"), "stop-first"),
		UpdateFailure:     orDefault(r.FormValue("update_failure_action"), "pause"),
	}
}

// parseNetworkAttachments resolves overlay network names (one per line) to
// attachment configs. Like secrets/configs, a service can only attach to a
// network that already exists - create it under Networks first.
func (s *Server) parseNetworkAttachments(ctx context.Context, names []string) ([]swarm.NetworkAttachmentConfig, error) {
	var out []swarm.NetworkAttachmentConfig
	for _, name := range names {
		nets, err := s.docker.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("name", name))})
		if err != nil {
			return nil, fmt.Errorf("look up network %q: %w", name, err)
		}
		if !hasExactNetwork(nets, name) {
			return nil, fmt.Errorf("network %q not found - create it under Networks first", name)
		}
		out = append(out, swarm.NetworkAttachmentConfig{Target: name})
	}
	return out, nil
}

// parseSecretRefs resolves "secretName" or "secretName:targetFile" lines
// (one per line) to secret attachments, mounted read-only under
// /run/secrets/<targetFile> (defaulting to the secret's own name).
func (s *Server) parseSecretRefs(ctx context.Context, lines []string) ([]*swarm.SecretReference, error) {
	var out []*swarm.SecretReference
	for _, line := range lines {
		name, target, ok := strings.Cut(line, ":")
		if !ok || target == "" {
			target = name
		}
		sec, err := s.findSecretByName(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, &swarm.SecretReference{
			SecretID:   sec.ID,
			SecretName: sec.Spec.Name,
			File: &swarm.SecretReferenceFileTarget{
				Name: target,
				UID:  "0",
				GID:  "0",
				Mode: 0o444,
			},
		})
	}
	return out, nil
}

// parseConfigRefs is parseSecretRefs' twin for configs.
func (s *Server) parseConfigRefs(ctx context.Context, lines []string) ([]*swarm.ConfigReference, error) {
	var out []*swarm.ConfigReference
	for _, line := range lines {
		name, target, ok := strings.Cut(line, ":")
		if !ok || target == "" {
			target = name
		}
		cfg, err := s.findConfigByName(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, &swarm.ConfigReference{
			ConfigID:   cfg.ID,
			ConfigName: cfg.Spec.Name,
			File: &swarm.ConfigReferenceFileTarget{
				Name: target,
				UID:  "0",
				GID:  "0",
				Mode: 0o444,
			},
		})
	}
	return out, nil
}

// parseHealthcheckForm builds a HealthConfig from the form, or nil if every
// field was left blank (inherit the image's own HEALTHCHECK, matching the
// no-op-when-unedited contract the rest of this form follows). Only the
// CMD-SHELL form is exposed in the UI since it's what compose's `test:
// <string>` shorthand and the vast majority of real healthchecks use.
func parseHealthcheckForm(r *http.Request) (*container.HealthConfig, error) {
	disabled := r.FormValue("health_disabled") == "on"
	test := strings.TrimSpace(r.FormValue("health_test"))
	interval := r.FormValue("health_interval")
	timeout := r.FormValue("health_timeout")
	startPeriod := r.FormValue("health_start_period")
	retries := r.FormValue("health_retries")

	if !disabled && test == "" && interval == "" && timeout == "" && startPeriod == "" && retries == "" {
		return nil, nil
	}

	hc := &container.HealthConfig{}
	if disabled {
		hc.Test = []string{"NONE"}
	} else if test != "" {
		hc.Test = []string{"CMD-SHELL", test}
	}

	var err error
	if interval != "" {
		if hc.Interval, err = time.ParseDuration(interval); err != nil {
			return nil, fmt.Errorf("invalid healthcheck interval: %w", err)
		}
	}
	if timeout != "" {
		if hc.Timeout, err = time.ParseDuration(timeout); err != nil {
			return nil, fmt.Errorf("invalid healthcheck timeout: %w", err)
		}
	}
	if startPeriod != "" {
		if hc.StartPeriod, err = time.ParseDuration(startPeriod); err != nil {
			return nil, fmt.Errorf("invalid healthcheck start period: %w", err)
		}
	}
	if retries != "" {
		n, err := strconv.Atoi(retries)
		if err != nil {
			return nil, fmt.Errorf("invalid healthcheck retries: %w", err)
		}
		hc.Retries = n
	}
	return hc, nil
}

func parseServiceResourceForm(r *http.Request) (*swarm.ResourceRequirements, error) {
	cpuLimit, memLimit := r.FormValue("cpu_limit"), r.FormValue("mem_limit")
	cpuRes, memRes := r.FormValue("cpu_reservation"), r.FormValue("mem_reservation")
	if cpuLimit == "" && memLimit == "" && cpuRes == "" && memRes == "" {
		return nil, nil
	}
	out := &swarm.ResourceRequirements{}
	if cpuLimit != "" || memLimit != "" {
		nano, mem, err := parseCPUMem(cpuLimit, memLimit)
		if err != nil {
			return nil, err
		}
		out.Limits = &swarm.Limit{NanoCPUs: nano, MemoryBytes: mem}
	}
	if cpuRes != "" || memRes != "" {
		nano, mem, err := parseCPUMem(cpuRes, memRes)
		if err != nil {
			return nil, err
		}
		out.Reservations = &swarm.Resources{NanoCPUs: nano, MemoryBytes: mem}
	}
	return out, nil
}

func parseLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// editorFormData holds a service's current spec pre-rendered as the plain
// strings the advanced editor's textareas/inputs use, so re-opening the
// form after a change shows the values that are actually live.
type editorFormData struct {
	Env               string
	Labels            string
	Mounts            string
	Constraints       string
	Preferences       string
	Networks          string
	Ports             string
	Secrets           string
	Configs           string
	Entrypoint        string
	Command           string
	HealthTest        string
	HealthDisabled    bool
	HealthInterval    string
	HealthTimeout     string
	HealthStartPeriod string
	HealthRetries     string
	CPULimit          string
	MemLimit          string
	CPUReservation    string
	MemReservation    string
	RestartCondition  string
	UpdateParallelism uint64
	UpdateDelay       string
	UpdateOrder       string
	UpdateFailure     string
}

// serviceEditorForm pre-fills the advanced editor from svc's current spec.
// netNames resolves an attached network's ID (how it's stored once a
// service is created) back to the name the user typed, so it round-trips
// through the textarea instead of showing a raw ID.
func serviceEditorForm(svc swarm.Service, netNames map[string]string) editorFormData {
	var d editorFormData
	cs := svc.Spec.TaskTemplate.ContainerSpec
	if cs != nil {
		d.Env = strings.Join(cs.Env, "\n")
		d.Labels = joinKV(cs.Labels)
		d.Entrypoint = strings.Join(cs.Command, "\n")
		d.Command = strings.Join(cs.Args, "\n")

		var mounts []string
		for _, m := range cs.Mounts {
			entry := m.Source + ":" + m.Target
			if m.ReadOnly {
				entry += ":ro"
			}
			mounts = append(mounts, entry)
		}
		d.Mounts = strings.Join(mounts, "\n")

		var secretLines []string
		for _, sr := range cs.Secrets {
			line := sr.SecretName
			if sr.File != nil && sr.File.Name != "" && sr.File.Name != sr.SecretName {
				line += ":" + sr.File.Name
			}
			secretLines = append(secretLines, line)
		}
		d.Secrets = strings.Join(secretLines, "\n")

		var configLines []string
		for _, cr := range cs.Configs {
			line := cr.ConfigName
			if cr.File != nil && cr.File.Name != "" && cr.File.Name != cr.ConfigName {
				line += ":" + cr.File.Name
			}
			configLines = append(configLines, line)
		}
		d.Configs = strings.Join(configLines, "\n")

		if hc := cs.Healthcheck; hc != nil {
			switch {
			case len(hc.Test) == 1 && hc.Test[0] == "NONE":
				d.HealthDisabled = true
			case len(hc.Test) > 1:
				d.HealthTest = strings.Join(hc.Test[1:], " ")
			}
			if hc.Interval > 0 {
				d.HealthInterval = hc.Interval.String()
			}
			if hc.Timeout > 0 {
				d.HealthTimeout = hc.Timeout.String()
			}
			if hc.StartPeriod > 0 {
				d.HealthStartPeriod = hc.StartPeriod.String()
			}
			if hc.Retries > 0 {
				d.HealthRetries = strconv.Itoa(hc.Retries)
			}
		}
	}

	var networks []string
	for _, na := range svc.Spec.TaskTemplate.Networks {
		name := na.Target
		if n, ok := netNames[na.Target]; ok && n != "" {
			name = n
		}
		networks = append(networks, name)
	}
	d.Networks = strings.Join(networks, "\n")

	if svc.Spec.EndpointSpec != nil {
		d.Ports = formatPorts(svc.Spec.EndpointSpec.Ports)
	}

	if p := svc.Spec.TaskTemplate.Placement; p != nil {
		d.Constraints = strings.Join(p.Constraints, "\n")
		var prefs []string
		for _, pref := range p.Preferences {
			if pref.Spread != nil {
				prefs = append(prefs, pref.Spread.SpreadDescriptor)
			}
		}
		d.Preferences = strings.Join(prefs, "\n")
	}
	if res := svc.Spec.TaskTemplate.Resources; res != nil {
		if res.Limits != nil {
			d.CPULimit = cpusToString(res.Limits.NanoCPUs)
			d.MemLimit = bytesToString(res.Limits.MemoryBytes)
		}
		if res.Reservations != nil {
			d.CPUReservation = cpusToString(res.Reservations.NanoCPUs)
			d.MemReservation = bytesToString(res.Reservations.MemoryBytes)
		}
	}
	d.RestartCondition = "any"
	if rp := svc.Spec.TaskTemplate.RestartPolicy; rp != nil && rp.Condition != "" {
		d.RestartCondition = string(rp.Condition)
	}
	d.UpdateOrder, d.UpdateFailure = "stop-first", "pause"
	if uc := svc.Spec.UpdateConfig; uc != nil {
		d.UpdateParallelism = uc.Parallelism
		d.UpdateDelay = uc.Delay.String()
		if uc.Order != "" {
			d.UpdateOrder = uc.Order
		}
		if uc.FailureAction != "" {
			d.UpdateFailure = uc.FailureAction
		}
	}
	return d
}

// formatPorts renders published port mappings back to the "8080:80" /
// "8080:80/udp" short syntax parsePorts (in compose_apply.go) accepts, so
// the ports textarea round-trips the service's current EndpointSpec.
func formatPorts(ports []swarm.PortConfig) string {
	lines := make([]string, 0, len(ports))
	for _, p := range ports {
		line := fmt.Sprintf("%d:%d", p.PublishedPort, p.TargetPort)
		if p.Protocol != "" && p.Protocol != swarm.PortConfigProtocolTCP {
			line += "/" + string(p.Protocol)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func joinKV(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	lines := make([]string, 0, len(m))
	for k, v := range m {
		lines = append(lines, k+"="+v)
	}
	return strings.Join(lines, "\n")
}

func parseKVLines(s string) map[string]string {
	lines := parseLines(s)
	if len(lines) == 0 {
		return nil
	}
	out := make(map[string]string, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			out[parts[0]] = parts[1]
		} else {
			out[parts[0]] = ""
		}
	}
	return out
}
