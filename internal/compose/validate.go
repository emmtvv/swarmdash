package compose

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// shape describes which keys a mapping at some position in a compose file
// may contain, so validate can report everything else instead of the YAML
// decoder silently dropping it.
type shape struct {
	// keys maps an allowed key to the shape of its value; a nil shape means
	// "accepted, don't look inside" (scalars, free-form maps like
	// environment, or values whose own UnmarshalYAML validates them).
	keys map[string]*shape
	// entries, if set, means this is a user-keyed mapping (services,
	// networks, ...) and every value has this shape.
	entries *shape
	// items, if set, means this is a list whose mapping entries (long
	// syntax) have this shape; scalar entries are short syntax.
	items *shape
	// hints explains why a known compose key is ignored.
	hints map[string]string
}

func keys(kv map[string]*shape) *shape { return &shape{keys: kv} }

var (
	updateConfigShape = keys(map[string]*shape{
		"parallelism": nil, "delay": nil, "order": nil, "failure_action": nil,
		"monitor": nil, "max_failure_ratio": nil,
	})
	resourceSpecShape = keys(map[string]*shape{"cpus": nil, "memory": nil, "pids": nil})
	fileRefShape      = keys(map[string]*shape{"source": nil, "target": nil, "uid": nil, "gid": nil, "mode": nil})

	serviceShape = &shape{
		keys: map[string]*shape{
			"image": nil, "entrypoint": nil, "command": nil, "environment": nil, "labels": nil,
			"user": nil, "working_dir": nil, "hostname": nil, "stop_grace_period": nil,
			"stop_signal": nil, "init": nil, "read_only": nil, "tty": nil, "cap_add": nil,
			"cap_drop": nil, "sysctls": nil, "ulimits": nil, "dns": nil, "extra_hosts": nil,
			"healthcheck": keys(map[string]*shape{
				"test": nil, "interval": nil, "timeout": nil, "start_period": nil,
				"start_interval": nil, "retries": nil, "disable": nil,
			}),
			"logging": keys(map[string]*shape{"driver": nil, "options": nil}),
			"ports":   {items: keys(map[string]*shape{"target": nil, "published": nil, "protocol": nil, "mode": nil, "host_ip": nil})},
			"volumes": {items: keys(map[string]*shape{
				"type": nil, "source": nil, "target": nil, "read_only": nil,
				"bind":   keys(map[string]*shape{"propagation": nil}),
				"volume": keys(map[string]*shape{"nocopy": nil}),
				"tmpfs":  keys(map[string]*shape{"size": nil, "mode": nil}),
			})},
			"networks": {entries: &shape{keys: map[string]*shape{"aliases": nil}}},
			"secrets":  {items: fileRefShape},
			"configs":  {items: fileRefShape},
			"deploy": keys(map[string]*shape{
				"replicas": nil, "mode": nil, "endpoint_mode": nil, "labels": nil,
				"placement":       keys(map[string]*shape{"constraints": nil, "preferences": nil, "max_replicas_per_node": nil}),
				"resources":       keys(map[string]*shape{"limits": resourceSpecShape, "reservations": resourceSpecShape}),
				"update_config":   updateConfigShape,
				"rollback_config": updateConfigShape,
				"restart_policy":  keys(map[string]*shape{"condition": nil, "delay": nil, "max_attempts": nil, "window": nil}),
			}),
		},
		hints: map[string]string{
			"build":          "swarm can't build images - build and push in CI, then reference the result under image:",
			"depends_on":     "swarm has no startup ordering between services",
			"container_name": "swarm names task containers itself",
			"restart":        "use deploy.restart_policy instead",
			"env_file":       "swarmdash can't read files next to the compose file - inline the values under environment:, or use ${VAR} references with the Variables field",
			"links":          "every service on a shared network is already reachable by name",
			"expose":         "every port is already reachable from services on the same overlay network",
			"profiles":       "profiles aren't supported - every service in the file is deployed",
			"privileged":     "swarm services can't run privileged",
			"network_mode":   "swarm services can only attach to networks",
			"devices":        "swarm services can't map host devices",
			"mem_limit":      "use deploy.resources.limits.memory instead",
			"cpus":           "use deploy.resources.limits.cpus instead",
			"pull_policy":    "swarm always resolves the image tag at deploy time",
			"platform":       "use a deploy.placement.constraints entry on node.platform instead",
		},
	}

	fileShape = &shape{
		keys: map[string]*shape{
			"version":  nil,
			"services": {entries: serviceShape},
			"networks": {entries: keys(map[string]*shape{
				"driver": nil, "driver_opts": nil, "external": nil, "attachable": nil,
				"internal": nil, "name": nil, "labels": nil,
			})},
			"volumes": {entries: keys(map[string]*shape{
				"driver": nil, "driver_opts": nil, "external": nil, "name": nil, "labels": nil,
			})},
			"secrets": {entries: &shape{
				keys:  map[string]*shape{"external": nil, "name": nil},
				hints: map[string]string{"file": "secrets must be created under Secrets first and referenced with external: true", "environment": "secrets must be created under Secrets first and referenced with external: true"},
			}},
			"configs": {entries: &shape{
				keys:  map[string]*shape{"external": nil, "name": nil, "content": nil},
				hints: map[string]string{"file": "swarmdash can't read files next to the compose file - inline the file under content: instead"},
			}},
		},
		hints: map[string]string{"name": "the stack name comes from the deploy form"},
	}
)

// validate walks a parsed compose document and returns one warning per key
// that swarmdash doesn't support. Extension keys (x-*) and YAML merge keys
// are always allowed.
func validate(doc *yaml.Node) []string {
	var warnings []string
	walkShape(doc, fileShape, "", &warnings)
	return warnings
}

func walkShape(n *yaml.Node, sh *shape, path string, warnings *[]string) {
	if n == nil || sh == nil {
		return
	}
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	switch {
	case sh.entries != nil:
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if key == "<<" || strings.HasPrefix(key, "x-") {
				continue
			}
			walkShape(n.Content[i+1], sh.entries, join(path, key), warnings)
		}
	case sh.items != nil:
		if n.Kind != yaml.SequenceNode {
			return
		}
		for i, item := range n.Content {
			if item.Kind == yaml.AliasNode {
				item = item.Alias
			}
			if item.Kind == yaml.MappingNode {
				walkShape(item, sh.items, fmt.Sprintf("%s[%d]", path, i), warnings)
			}
		}
	default:
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if key == "<<" || strings.HasPrefix(key, "x-") {
				continue
			}
			child, ok := sh.keys[key]
			if !ok {
				msg := fmt.Sprintf("line %d: %s is not supported and was ignored", n.Content[i].Line, join(path, key))
				if hint := sh.hints[key]; hint != "" {
					msg += " (" + hint + ")"
				}
				*warnings = append(*warnings, msg)
				continue
			}
			walkShape(n.Content[i+1], child, join(path, key), warnings)
		}
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
