// Package compose implements a pragmatic subset of the Docker Compose file
// format - enough to round-trip what a swarm service actually is (image,
// entrypoint/command, env, labels, mounts, networks, secrets/configs,
// healthcheck, resources, replicas, update/rollback policy, ...). It
// intentionally does not support compose features that make no sense for an
// already-running swarm cluster (build:, depends_on: startup ordering,
// container_name, profiles, etc.) - those need real `docker stack deploy` /
// a build pipeline, not an admin panel. Load reports any such key it sees
// as a warning instead of silently dropping it (see validate.go).
package compose

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type File struct {
	Version  string                 `yaml:"version,omitempty"`
	Services map[string]Service     `yaml:"services"`
	Networks map[string]NetworkDef  `yaml:"networks,omitempty"`
	Volumes  map[string]VolumeDef   `yaml:"volumes,omitempty"`
	Secrets  map[string]ExternalRef `yaml:"secrets,omitempty"`
	Configs  map[string]ConfigDef   `yaml:"configs,omitempty"`
}

type Service struct {
	Image           string            `yaml:"image"`
	Entrypoint      StrList           `yaml:"entrypoint,omitempty"`
	Command         StrList           `yaml:"command,omitempty"`
	Environment     EnvMap            `yaml:"environment,omitempty"`
	Labels          EnvMap            `yaml:"labels,omitempty"`
	User            string            `yaml:"user,omitempty"`
	WorkingDir      string            `yaml:"working_dir,omitempty"`
	Hostname        string            `yaml:"hostname,omitempty"`
	StopGracePeriod string            `yaml:"stop_grace_period,omitempty"`
	StopSignal      string            `yaml:"stop_signal,omitempty"`
	Init            *bool             `yaml:"init,omitempty"`
	ReadOnly        bool              `yaml:"read_only,omitempty"`
	TTY             bool              `yaml:"tty,omitempty"`
	CapAdd          []string          `yaml:"cap_add,omitempty"`
	CapDrop         []string          `yaml:"cap_drop,omitempty"`
	Sysctls         EnvMap            `yaml:"sysctls,omitempty"`
	Ulimits         map[string]Ulimit `yaml:"ulimits,omitempty"`
	DNS             StrList           `yaml:"dns,omitempty"`
	ExtraHosts      HostList          `yaml:"extra_hosts,omitempty"`
	Healthcheck     *Healthcheck      `yaml:"healthcheck,omitempty"`
	Logging         *Logging          `yaml:"logging,omitempty"`
	Ports           PortList          `yaml:"ports,omitempty"`
	Volumes         []VolumeMount     `yaml:"volumes,omitempty"`
	Networks        ServiceNetworks   `yaml:"networks,omitempty"`
	Secrets         []FileRef         `yaml:"secrets,omitempty"`
	Configs         []FileRef         `yaml:"configs,omitempty"`
	Deploy          Deploy            `yaml:"deploy,omitempty"`
}

type Deploy struct {
	Replicas       *uint64        `yaml:"replicas,omitempty"`
	Mode           string         `yaml:"mode,omitempty"` // "replicated" (default) or "global"
	EndpointMode   string         `yaml:"endpoint_mode,omitempty"`
	Labels         EnvMap         `yaml:"labels,omitempty"`
	Placement      Placement      `yaml:"placement,omitempty"`
	Resources      Resources      `yaml:"resources,omitempty"`
	UpdateConfig   *UpdateConfig  `yaml:"update_config,omitempty"`
	RollbackConfig *UpdateConfig  `yaml:"rollback_config,omitempty"`
	RestartPolicy  *RestartPolicy `yaml:"restart_policy,omitempty"`
}

type Placement struct {
	Constraints        []string     `yaml:"constraints,omitempty"`
	Preferences        []Preference `yaml:"preferences,omitempty"`
	MaxReplicasPerNode uint64       `yaml:"max_replicas_per_node,omitempty"`
}

// Preference mirrors compose's `- spread: node.labels.zone` list form.
type Preference struct {
	Spread string `yaml:"spread,omitempty"`
}

type Resources struct {
	Limits       *ResourceSpec `yaml:"limits,omitempty"`
	Reservations *ResourceSpec `yaml:"reservations,omitempty"`
}

type ResourceSpec struct {
	CPUs   string `yaml:"cpus,omitempty"`
	Memory string `yaml:"memory,omitempty"`
	Pids   int64  `yaml:"pids,omitempty"`
}

// UpdateConfig is used for both deploy.update_config and
// deploy.rollback_config, which share a schema.
type UpdateConfig struct {
	Parallelism     int     `yaml:"parallelism,omitempty"`
	Delay           string  `yaml:"delay,omitempty"`
	Order           string  `yaml:"order,omitempty"`
	FailureAction   string  `yaml:"failure_action,omitempty"`
	Monitor         string  `yaml:"monitor,omitempty"`
	MaxFailureRatio float32 `yaml:"max_failure_ratio,omitempty"`
}

type RestartPolicy struct {
	Condition   string  `yaml:"condition,omitempty"`
	Delay       string  `yaml:"delay,omitempty"`
	MaxAttempts *uint64 `yaml:"max_attempts,omitempty"`
	Window      string  `yaml:"window,omitempty"`
}

type Healthcheck struct {
	Test          HealthTest `yaml:"test,omitempty"`
	Interval      string     `yaml:"interval,omitempty"`
	Timeout       string     `yaml:"timeout,omitempty"`
	StartPeriod   string     `yaml:"start_period,omitempty"`
	StartInterval string     `yaml:"start_interval,omitempty"`
	Retries       *int       `yaml:"retries,omitempty"`
	Disable       bool       `yaml:"disable,omitempty"`
}

type Logging struct {
	Driver  string            `yaml:"driver,omitempty"`
	Options map[string]string `yaml:"options,omitempty"`
}

type NetworkDef struct {
	Driver     string            `yaml:"driver,omitempty"`
	DriverOpts map[string]string `yaml:"driver_opts,omitempty"`
	External   bool              `yaml:"external,omitempty"`
	Attachable bool              `yaml:"attachable,omitempty"`
	Internal   bool              `yaml:"internal,omitempty"`
	Name       string            `yaml:"name,omitempty"`
	Labels     EnvMap            `yaml:"labels,omitempty"`
}

// VolumeDef is a top-level `volumes:` entry. Its driver/driver_opts travel
// with every service mount that references it (Swarm creates the volume
// lazily on whichever node a task lands on), which is how NFS/CIFS-backed
// volumes work in a swarm.
type VolumeDef struct {
	Driver     string            `yaml:"driver,omitempty"`
	DriverOpts map[string]string `yaml:"driver_opts,omitempty"`
	External   bool              `yaml:"external,omitempty"`
	Name       string            `yaml:"name,omitempty"`
	Labels     EnvMap            `yaml:"labels,omitempty"`
}

// ExternalRef is used for secrets: swarmdash only attaches to secrets that
// already exist (created via the Secrets page), it never creates them from
// compose content - that content would have to live in the compose file
// itself, defeating the point of storing secrets encrypted and write-only.
type ExternalRef struct {
	External bool   `yaml:"external,omitempty"`
	Name     string `yaml:"name,omitempty"`
}

// ConfigDef is a top-level `configs:` entry: either a reference to an
// existing config (external/name), or inline `content:` that swarmdash
// creates as a content-addressed config for the stack. Configs aren't
// secret, so unlike secrets their content may live in the compose file.
type ConfigDef struct {
	External bool   `yaml:"external,omitempty"`
	Name     string `yaml:"name,omitempty"`
	Content  string `yaml:"content,omitempty"`
}

// Parse reads a compose file's bytes into the subset schema above, with
// variable interpolation against an empty variable set. Use Load to supply
// variables and get back warnings about unsupported keys.
func Parse(data []byte) (*File, error) {
	f, _, err := Load(data, nil)
	return f, err
}

// Load parses a compose file, interpolating ${VAR}-style references from
// vars (see interpolate.go), and returns warnings for every key swarmdash
// doesn't act on (unsupported compose features, typos) and every variable
// that was referenced but not set.
func Load(data []byte, vars map[string]string) (*File, []string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("parse compose file: %w", err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, nil, fmt.Errorf("compose file defines no services")
	}
	doc := root.Content[0]

	missing, err := interpolateNode(doc, vars)
	if err != nil {
		return nil, nil, err
	}
	warnings := validate(doc)
	for _, name := range missing {
		warnings = append(warnings, fmt.Sprintf("variable %s is not set - substituted an empty string", name))
	}

	var f File
	if err := doc.Decode(&f); err != nil {
		return nil, nil, fmt.Errorf("parse compose file: %w", err)
	}
	if len(f.Services) == 0 {
		return nil, nil, fmt.Errorf("compose file defines no services")
	}
	return &f, warnings, nil
}

// EnvMap accepts both compose forms for environment/labels: a YAML mapping
// (KEY: value) or a list of "KEY=value" strings.
type EnvMap map[string]string

func (e *EnvMap) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
		// Decoding into map[string]string (rather than walking Content by
		// hand) keeps YAML merge keys (`<<: *common-env`) working and turns
		// a bare `KEY:` (null) into "".
		var m map[string]string
		if err := node.Decode(&m); err != nil {
			return err
		}
		*e = m
	case yaml.SequenceNode:
		var list []string
		if err := node.Decode(&list); err != nil {
			return err
		}
		m := make(map[string]string, len(list))
		for _, kv := range list {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) == 2 {
				m[parts[0]] = parts[1]
			} else {
				m[parts[0]] = ""
			}
		}
		*e = m
	case 0:
		// absent key - leave nil
	default:
		return fmt.Errorf("unsupported YAML node kind %v for environment/labels", node.Kind)
	}
	return nil
}

// StrList accepts both a single scalar string and a YAML sequence, since
// compose allows `command: foo` or `command: [foo, bar]`. The scalar form is
// split shell-style (quotes group words), matching what compose itself does
// with `command: sh -c "echo hi"`.
type StrList []string

func (s *StrList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var v string
		if err := node.Decode(&v); err != nil {
			return err
		}
		words, err := splitShellWords(v)
		if err != nil {
			return fmt.Errorf("line %d: %w", node.Line, err)
		}
		*s = words
	case yaml.SequenceNode:
		var v []string
		if err := node.Decode(&v); err != nil {
			return err
		}
		*s = v
	case 0:
	default:
		return fmt.Errorf("unsupported YAML node kind %v for string list", node.Kind)
	}
	return nil
}

// splitShellWords splits s the way a POSIX shell would split a simple
// command line: whitespace separates words, single quotes are literal,
// double quotes group but honor backslash escapes, and a bare backslash
// escapes the next character. No expansion of any kind is performed.
func splitShellWords(s string) ([]string, error) {
	var (
		words   []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in %q", s)
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// HostList accepts extra_hosts as a list ("host:ip" / "host=ip") or a
// mapping (host: ip), normalized to "host:ip" entries.
type HostList []string

func (h *HostList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var v []string
		if err := node.Decode(&v); err != nil {
			return err
		}
		out := make([]string, 0, len(v))
		for _, e := range v {
			if host, ip, ok := strings.Cut(e, "="); ok {
				e = host + ":" + ip
			}
			out = append(out, e)
		}
		*h = out
	case yaml.MappingNode:
		out := make([]string, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			out = append(out, node.Content[i].Value+":"+node.Content[i+1].Value)
		}
		*h = out
	case 0:
	default:
		return fmt.Errorf("line %d: extra_hosts must be a list or mapping", node.Line)
	}
	return nil
}

// HealthTest accepts compose's two healthcheck test forms: a string (run
// via the container's shell, i.e. CMD-SHELL) or an explicit list whose first
// element is CMD, CMD-SHELL or NONE.
type HealthTest []string

func (t *HealthTest) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*t = HealthTest{"CMD-SHELL", node.Value}
	case yaml.SequenceNode:
		var v []string
		if err := node.Decode(&v); err != nil {
			return err
		}
		*t = v
	case 0:
	default:
		return fmt.Errorf("line %d: healthcheck test must be a string or list", node.Line)
	}
	return nil
}

func (t HealthTest) MarshalYAML() (any, error) {
	if len(t) == 2 && t[0] == "CMD-SHELL" {
		return t[1], nil
	}
	return []string(t), nil
}

// Ulimit accepts both `nofile: 65535` and `nofile: {soft: 1024, hard: 2048}`.
type Ulimit struct {
	Soft int64 `yaml:"soft"`
	Hard int64 `yaml:"hard"`
}

func (u *Ulimit) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		n, err := strconv.ParseInt(node.Value, 10, 64)
		if err != nil {
			return fmt.Errorf("line %d: invalid ulimit %q", node.Line, node.Value)
		}
		u.Soft, u.Hard = n, n
		return nil
	}
	type plain Ulimit
	return node.Decode((*plain)(u))
}

func (u Ulimit) MarshalYAML() (any, error) {
	if u.Soft == u.Hard {
		return u.Soft, nil
	}
	type plain Ulimit
	return plain(u), nil
}

// ServiceNetwork is one entry of a service's `networks:`, which compose
// allows either as a plain list of names or as a mapping of name to
// per-attachment options (only aliases are supported).
type ServiceNetwork struct {
	Name    string
	Aliases []string
}

type ServiceNetworks []ServiceNetwork

func (n *ServiceNetworks) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*n = ServiceNetworks{{Name: node.Value}}
	case yaml.SequenceNode:
		var names []string
		if err := node.Decode(&names); err != nil {
			return err
		}
		out := make(ServiceNetworks, 0, len(names))
		for _, name := range names {
			out = append(out, ServiceNetwork{Name: name})
		}
		*n = out
	case yaml.MappingNode:
		out := make(ServiceNetworks, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			sn := ServiceNetwork{Name: node.Content[i].Value}
			if v := node.Content[i+1]; v.Kind == yaml.MappingNode {
				var opts struct {
					Aliases []string `yaml:"aliases"`
				}
				if err := v.Decode(&opts); err != nil {
					return err
				}
				sn.Aliases = opts.Aliases
			}
			out = append(out, sn)
		}
		*n = out
	case 0:
	default:
		return fmt.Errorf("line %d: networks must be a list or mapping", node.Line)
	}
	return nil
}

func (n ServiceNetworks) MarshalYAML() (any, error) {
	hasAliases := false
	for _, sn := range n {
		if len(sn.Aliases) > 0 {
			hasAliases = true
		}
	}
	if !hasAliases {
		names := make([]string, 0, len(n))
		for _, sn := range n {
			names = append(names, sn.Name)
		}
		return names, nil
	}
	out := &yaml.Node{Kind: yaml.MappingNode}
	for _, sn := range n {
		val := &yaml.Node{Kind: yaml.MappingNode}
		if len(sn.Aliases) > 0 {
			var aliases yaml.Node
			if err := aliases.Encode(sn.Aliases); err != nil {
				return nil, err
			}
			val.Content = append(val.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "aliases"}, &aliases)
		}
		out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: sn.Name}, val)
	}
	return out, nil
}

// Names returns just the network keys, in order.
func (n ServiceNetworks) Names() []string {
	out := make([]string, 0, len(n))
	for _, sn := range n {
		out = append(out, sn.Name)
	}
	return out
}
