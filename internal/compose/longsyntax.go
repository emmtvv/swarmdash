package compose

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Port is one published port. Compose allows both the short string syntax
// ("8080:80", "8080:80/udp", "8000-8001:80-81") and the long mapping
// syntax ({target, published, protocol, mode}); a short-syntax range
// expands to one Port per port in it, which is why a service's ports are a
// PortList rather than a plain []Port.
type Port struct {
	Target    uint32 `yaml:"target"`
	Published uint32 `yaml:"published,omitempty"`
	Protocol  string `yaml:"protocol,omitempty"` // "tcp" (default), "udp", "sctp"
	Mode      string `yaml:"mode,omitempty"`     // "ingress" (default) or "host"
}

type PortList []Port

func (p *PortList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: ports must be a list", node.Line)
	}
	var out PortList
	for _, item := range node.Content {
		if item.Kind == yaml.AliasNode {
			item = item.Alias
		}
		switch item.Kind {
		case yaml.ScalarNode:
			ports, err := ParsePortSpec(item.Value)
			if err != nil {
				return fmt.Errorf("line %d: %w", item.Line, err)
			}
			out = append(out, ports...)
		case yaml.MappingNode:
			var long struct {
				Target    uint32 `yaml:"target"`
				Published string `yaml:"published"`
				Protocol  string `yaml:"protocol"`
				Mode      string `yaml:"mode"`
				HostIP    string `yaml:"host_ip"`
			}
			if err := item.Decode(&long); err != nil {
				return err
			}
			if long.Target == 0 {
				return fmt.Errorf("line %d: port is missing target", item.Line)
			}
			if long.HostIP != "" {
				return fmt.Errorf("line %d: host_ip is not supported by swarm services", item.Line)
			}
			port := Port{Target: long.Target, Protocol: long.Protocol, Mode: long.Mode}
			if long.Published != "" {
				n, err := strconv.ParseUint(long.Published, 10, 16)
				if err != nil {
					return fmt.Errorf("line %d: invalid published port %q", item.Line, long.Published)
				}
				port.Published = uint32(n)
			}
			if err := checkPortEnums(port); err != nil {
				return fmt.Errorf("line %d: %w", item.Line, err)
			}
			out = append(out, port)
		default:
			return fmt.Errorf("line %d: invalid port entry", item.Line)
		}
	}
	*p = out
	return nil
}

func (p Port) MarshalYAML() (any, error) {
	if p.Mode == "" || p.Mode == "ingress" {
		return p.String(), nil
	}
	type plain Port
	return plain(p), nil
}

// String renders p in the short "published:target[/proto]" syntax
// ParsePortSpec accepts (dropping a non-default mode, which short syntax
// can't express).
func (p Port) String() string {
	s := fmt.Sprintf("%d:%d", p.Published, p.Target)
	if p.Published == 0 {
		s = strconv.FormatUint(uint64(p.Target), 10)
	}
	if p.Protocol != "" && p.Protocol != "tcp" {
		s += "/" + p.Protocol
	}
	return s
}

func checkPortEnums(p Port) error {
	switch p.Protocol {
	case "", "tcp", "udp", "sctp":
	default:
		return fmt.Errorf("invalid port protocol %q", p.Protocol)
	}
	switch p.Mode {
	case "", "ingress", "host":
	default:
		return fmt.Errorf("invalid port mode %q (want ingress or host)", p.Mode)
	}
	return nil
}

// ParsePortSpec parses compose's short port syntax: "80" (published on the
// same port, swarmdash's long-standing convention), "8080:80",
// "8080:80/udp", and ranges like "8000-8010:8000-8010" (expanded to one
// Port each). A leading host IP ("127.0.0.1:8080:80") is rejected - swarm's
// routing mesh always binds every address.
func ParsePortSpec(spec string) ([]Port, error) {
	raw := spec
	proto := ""
	if i := strings.LastIndex(spec, "/"); i != -1 {
		proto = spec[i+1:]
		spec = spec[:i]
	}
	parts := strings.Split(spec, ":")
	var pubSpec, targetSpec string
	switch len(parts) {
	case 1:
		pubSpec, targetSpec = parts[0], parts[0]
	case 2:
		pubSpec, targetSpec = parts[0], parts[1]
	case 3:
		return nil, fmt.Errorf("invalid port mapping %q: binding to a host IP is not supported by swarm services", raw)
	default:
		return nil, fmt.Errorf("invalid port mapping %q", raw)
	}
	pubLo, pubHi, err := parsePortRange(pubSpec)
	if err != nil {
		return nil, fmt.Errorf("invalid port mapping %q: %w", raw, err)
	}
	tLo, tHi, err := parsePortRange(targetSpec)
	if err != nil {
		return nil, fmt.Errorf("invalid port mapping %q: %w", raw, err)
	}
	if pubHi-pubLo != tHi-tLo {
		return nil, fmt.Errorf("invalid port mapping %q: published and target ranges differ in size", raw)
	}
	var out []Port
	for i := uint32(0); i <= tHi-tLo; i++ {
		p := Port{Target: tLo + i, Published: pubLo + i, Protocol: proto}
		if err := checkPortEnums(p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func parsePortRange(s string) (lo, hi uint32, err error) {
	loS, hiS, isRange := strings.Cut(s, "-")
	l, err := strconv.ParseUint(loS, 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port %q", loS)
	}
	if !isRange {
		return uint32(l), uint32(l), nil
	}
	h, err := strconv.ParseUint(hiS, 10, 16)
	if err != nil || h < l {
		return 0, 0, fmt.Errorf("invalid port range %q", s)
	}
	return uint32(l), uint32(h), nil
}

// VolumeMount is one entry of a service's `volumes:`, in either compose's
// short syntax ("target", "source:target[:opts]") or long syntax.
type VolumeMount struct {
	Type     string        `yaml:"type"` // "volume", "bind" or "tmpfs"
	Source   string        `yaml:"source,omitempty"`
	Target   string        `yaml:"target"`
	ReadOnly bool          `yaml:"read_only,omitempty"`
	Bind     *BindOptions  `yaml:"bind,omitempty"`
	Volume   *VolumeOpts   `yaml:"volume,omitempty"`
	Tmpfs    *TmpfsOptions `yaml:"tmpfs,omitempty"`
}

type BindOptions struct {
	Propagation string `yaml:"propagation,omitempty"`
}

type VolumeOpts struct {
	NoCopy bool `yaml:"nocopy,omitempty"`
}

type TmpfsOptions struct {
	Size string `yaml:"size,omitempty"` // bytes, or a unit string like "64m"
	Mode uint32 `yaml:"mode,omitempty"`
}

func (v *VolumeMount) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		m, err := ParseVolumeSpec(node.Value)
		if err != nil {
			return fmt.Errorf("line %d: %w", node.Line, err)
		}
		*v = m
		return nil
	}
	type plain VolumeMount
	var m plain
	if err := node.Decode(&m); err != nil {
		return err
	}
	if m.Target == "" {
		return fmt.Errorf("line %d: volume is missing target", node.Line)
	}
	switch m.Type {
	case "":
		m.Type = "volume"
	case "volume", "bind", "tmpfs":
	default:
		return fmt.Errorf("line %d: unsupported volume type %q (want volume, bind or tmpfs)", node.Line, m.Type)
	}
	*v = VolumeMount(m)
	return nil
}

func (v VolumeMount) MarshalYAML() (any, error) {
	if v.Tmpfs == nil && v.Type != "tmpfs" && (v.Bind == nil || v.Bind.Propagation == "") && v.Source != "" {
		s := v.Source + ":" + v.Target
		var opts []string
		if v.ReadOnly {
			opts = append(opts, "ro")
		}
		if v.Volume != nil && v.Volume.NoCopy {
			opts = append(opts, "nocopy")
		}
		if len(opts) > 0 {
			s += ":" + strings.Join(opts, ",")
		}
		return s, nil
	}
	type plain VolumeMount
	return plain(v), nil
}

// ParseVolumeSpec parses compose's short volume syntax: "target" (an
// anonymous volume), "source:target", or "source:target:opts" where opts is
// a comma-separated list of ro/rw/nocopy/z/Z and a bind propagation mode. A
// source starting with "/", "." or "~" is a bind mount; otherwise it's a
// named volume.
func ParseVolumeSpec(spec string) (VolumeMount, error) {
	parts := strings.Split(spec, ":")
	var m VolumeMount
	switch len(parts) {
	case 1:
		return VolumeMount{Type: "volume", Target: parts[0]}, nil
	case 2, 3:
		m = VolumeMount{Source: parts[0], Target: parts[1], Type: "volume"}
		if strings.HasPrefix(parts[0], "/") || strings.HasPrefix(parts[0], ".") || strings.HasPrefix(parts[0], "~") {
			m.Type = "bind"
		}
	default:
		return m, fmt.Errorf("invalid volume mapping %q", spec)
	}
	if m.Source == "" || m.Target == "" {
		return m, fmt.Errorf("invalid volume mapping %q", spec)
	}
	if len(parts) == 3 {
		for _, opt := range strings.Split(parts[2], ",") {
			switch opt {
			case "ro":
				m.ReadOnly = true
			case "rw", "z", "Z", "":
			case "nocopy":
				m.Volume = &VolumeOpts{NoCopy: true}
			case "shared", "rshared", "slave", "rslave", "private", "rprivate":
				m.Bind = &BindOptions{Propagation: opt}
			default:
				return m, fmt.Errorf("invalid volume mapping %q: unknown option %q", spec, opt)
			}
		}
	}
	return m, nil
}

// FileRef is a service's reference to a secret or config: just its
// top-level key, or the long form with a target path and file ownership.
type FileRef struct {
	Source string  `yaml:"source"`
	Target string  `yaml:"target,omitempty"`
	UID    string  `yaml:"uid,omitempty"`
	GID    string  `yaml:"gid,omitempty"`
	Mode   *uint32 `yaml:"mode,omitempty"`
}

func (f *FileRef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*f = FileRef{Source: node.Value}
		return nil
	}
	var long struct {
		Source string `yaml:"source"`
		Target string `yaml:"target"`
		UID    string `yaml:"uid"`
		GID    string `yaml:"gid"`
		Mode   string `yaml:"mode"`
	}
	if err := node.Decode(&long); err != nil {
		return err
	}
	if long.Source == "" {
		return fmt.Errorf("line %d: secret/config reference is missing source", node.Line)
	}
	*f = FileRef{Source: long.Source, Target: long.Target, UID: long.UID, GID: long.GID}
	if long.Mode != "" {
		// YAML 1.1-style octal (0440) and 0o440 both mean octal here, as
		// they do to compose; a plain decimal like 288 is taken literally.
		base := 10
		s := long.Mode
		if strings.HasPrefix(s, "0o") {
			s, base = s[2:], 8
		} else if len(s) > 1 && s[0] == '0' {
			base = 8
		}
		n, err := strconv.ParseUint(s, base, 32)
		if err != nil {
			return fmt.Errorf("line %d: invalid file mode %q", node.Line, long.Mode)
		}
		mode := uint32(n)
		f.Mode = &mode
	}
	return nil
}

func (f FileRef) MarshalYAML() (any, error) {
	if f.Target == "" && f.UID == "" && f.GID == "" && f.Mode == nil {
		return f.Source, nil
	}
	n := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k, v string, style yaml.Style) {
		n.Content = append(n.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Value: v, Style: style})
	}
	add("source", f.Source, 0)
	if f.Target != "" {
		add("target", f.Target, 0)
	}
	if f.UID != "" {
		add("uid", f.UID, yaml.DoubleQuotedStyle)
	}
	if f.GID != "" {
		add("gid", f.GID, yaml.DoubleQuotedStyle)
	}
	if f.Mode != nil {
		add("mode", fmt.Sprintf("0%o", *f.Mode), 0)
	}
	return n, nil
}
