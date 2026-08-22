// Package compose implements a pragmatic subset of the Docker Compose file
// format - just enough to round-trip what a swarm service actually is
// (image, env, labels, mounts, networks, resources, replicas, update
// policy). It intentionally does not support compose features that make no
// sense for an already-running swarm cluster (build:, depends_on: startup
// ordering, host-only bind semantics, profiles, etc.) - those need real
// `docker stack deploy` / a build pipeline, not an admin panel.
package compose

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type File struct {
	Version  string                 `yaml:"version,omitempty"`
	Services map[string]Service     `yaml:"services"`
	Networks map[string]NetworkDef  `yaml:"networks,omitempty"`
	Secrets  map[string]ExternalRef `yaml:"secrets,omitempty"`
	Configs  map[string]ExternalRef `yaml:"configs,omitempty"`
}

type Service struct {
	Image       string   `yaml:"image"`
	Command     StrList  `yaml:"command,omitempty"`
	Environment EnvMap   `yaml:"environment,omitempty"`
	Labels      EnvMap   `yaml:"labels,omitempty"`
	Ports       []string `yaml:"ports,omitempty"`
	Volumes     []string `yaml:"volumes,omitempty"`
	Networks    StrList  `yaml:"networks,omitempty"`
	Secrets     []string `yaml:"secrets,omitempty"`
	Configs     []string `yaml:"configs,omitempty"`
	Deploy      Deploy   `yaml:"deploy,omitempty"`
}

type Deploy struct {
	Replicas      *uint64        `yaml:"replicas,omitempty"`
	Mode          string         `yaml:"mode,omitempty"` // "replicated" (default) or "global"
	Labels        EnvMap         `yaml:"labels,omitempty"`
	Placement     Placement      `yaml:"placement,omitempty"`
	Resources     Resources      `yaml:"resources,omitempty"`
	UpdateConfig  *UpdateConfig  `yaml:"update_config,omitempty"`
	RestartPolicy *RestartPolicy `yaml:"restart_policy,omitempty"`
}

type Placement struct {
	Constraints []string     `yaml:"constraints,omitempty"`
	Preferences []Preference `yaml:"preferences,omitempty"`
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
}

type UpdateConfig struct {
	Parallelism   int    `yaml:"parallelism,omitempty"`
	Delay         string `yaml:"delay,omitempty"`
	Order         string `yaml:"order,omitempty"`
	FailureAction string `yaml:"failure_action,omitempty"`
}

type RestartPolicy struct {
	Condition string `yaml:"condition,omitempty"`
}

type NetworkDef struct {
	Driver     string `yaml:"driver,omitempty"`
	External   bool   `yaml:"external,omitempty"`
	Attachable bool   `yaml:"attachable,omitempty"`
	Name       string `yaml:"name,omitempty"`
}

// ExternalRef is used for secrets/configs: swarmdash only attaches to
// resources that already exist (created via the Secrets/Configs pages),
// it never creates them from inline compose content - that content would
// have to live in the compose file itself, defeating the point of storing
// secrets encrypted and write-only.
type ExternalRef struct {
	External bool   `yaml:"external,omitempty"`
	Name     string `yaml:"name,omitempty"`
}

// Parse reads a compose file's bytes into the subset schema above.
func Parse(data []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse compose file: %w", err)
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("compose file defines no services")
	}
	return &f, nil
}

// EnvMap accepts both compose forms for environment/labels: a YAML mapping
// (KEY: value) or a list of "KEY=value" strings.
type EnvMap map[string]string

func (e *EnvMap) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
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
// compose allows `command: foo` or `command: [foo, bar]` and similarly for
// a service's `networks:` list.
type StrList []string

func (s *StrList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var v string
		if err := node.Decode(&v); err != nil {
			return err
		}
		*s = strings.Fields(v)
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
