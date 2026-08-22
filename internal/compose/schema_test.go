package compose

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParse_Full(t *testing.T) {
	data := []byte(`
version: "3.8"
services:
  web:
    image: nginx:alpine
    command: ["nginx", "-g", "daemon off;"]
    environment:
      FOO: bar
      BAZ: qux
    labels:
      app: web
    ports:
      - "8080:80"
    volumes:
      - "data:/var/www"
    networks:
      - front
    deploy:
      replicas: 3
      mode: replicated
      placement:
        constraints:
          - "node.role==worker"
        preferences:
          - spread: node.labels.zone
      resources:
        limits:
          cpus: "1.0"
          memory: "512M"
        reservations:
          cpus: "0.5"
          memory: "256M"
      update_config:
        parallelism: 1
        delay: 10s
        order: start-first
        failure_action: rollback
      restart_policy:
        condition: on-failure
  worker:
    image: worker:latest
    command: run-worker --verbose
    environment:
      - "KEY1=value1"
      - "KEY2=value2"
networks:
  front:
    external: true
    name: front-net
secrets:
  db_password:
    external: true
    name: db_password
configs:
  app_config:
    external: true
    name: app_config
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Version != "3.8" {
		t.Fatalf("Version = %q, want 3.8", f.Version)
	}
	if len(f.Services) != 2 {
		t.Fatalf("len(Services) = %d, want 2", len(f.Services))
	}

	web, ok := f.Services["web"]
	if !ok {
		t.Fatal(`missing service "web"`)
	}
	if web.Image != "nginx:alpine" {
		t.Fatalf("web.Image = %q", web.Image)
	}
	wantCmd := StrList{"nginx", "-g", "daemon off;"}
	if !equalStrList(web.Command, wantCmd) {
		t.Fatalf("web.Command = %v, want %v", web.Command, wantCmd)
	}
	if web.Environment["FOO"] != "bar" || web.Environment["BAZ"] != "qux" {
		t.Fatalf("web.Environment = %v", web.Environment)
	}
	if web.Labels["app"] != "web" {
		t.Fatalf("web.Labels = %v", web.Labels)
	}
	if len(web.Ports) != 1 || web.Ports[0] != "8080:80" {
		t.Fatalf("web.Ports = %v", web.Ports)
	}
	if len(web.Volumes) != 1 || web.Volumes[0] != "data:/var/www" {
		t.Fatalf("web.Volumes = %v", web.Volumes)
	}
	if !equalStrList(web.Networks, StrList{"front"}) {
		t.Fatalf("web.Networks = %v", web.Networks)
	}

	if web.Deploy.Replicas == nil || *web.Deploy.Replicas != 3 {
		t.Fatalf("web.Deploy.Replicas = %v", web.Deploy.Replicas)
	}
	if web.Deploy.Mode != "replicated" {
		t.Fatalf("web.Deploy.Mode = %q", web.Deploy.Mode)
	}
	if len(web.Deploy.Placement.Constraints) != 1 || web.Deploy.Placement.Constraints[0] != "node.role==worker" {
		t.Fatalf("web.Deploy.Placement.Constraints = %v", web.Deploy.Placement.Constraints)
	}
	if len(web.Deploy.Placement.Preferences) != 1 || web.Deploy.Placement.Preferences[0].Spread != "node.labels.zone" {
		t.Fatalf("web.Deploy.Placement.Preferences = %v", web.Deploy.Placement.Preferences)
	}
	if web.Deploy.Resources.Limits == nil || web.Deploy.Resources.Limits.CPUs != "1.0" || web.Deploy.Resources.Limits.Memory != "512M" {
		t.Fatalf("web.Deploy.Resources.Limits = %+v", web.Deploy.Resources.Limits)
	}
	if web.Deploy.Resources.Reservations == nil || web.Deploy.Resources.Reservations.CPUs != "0.5" || web.Deploy.Resources.Reservations.Memory != "256M" {
		t.Fatalf("web.Deploy.Resources.Reservations = %+v", web.Deploy.Resources.Reservations)
	}
	if web.Deploy.UpdateConfig == nil {
		t.Fatal("web.Deploy.UpdateConfig is nil")
	} else {
		uc := web.Deploy.UpdateConfig
		if uc.Parallelism != 1 || uc.Delay != "10s" || uc.Order != "start-first" || uc.FailureAction != "rollback" {
			t.Fatalf("web.Deploy.UpdateConfig = %+v", uc)
		}
	}
	if web.Deploy.RestartPolicy == nil || web.Deploy.RestartPolicy.Condition != "on-failure" {
		t.Fatalf("web.Deploy.RestartPolicy = %+v", web.Deploy.RestartPolicy)
	}

	worker, ok := f.Services["worker"]
	if !ok {
		t.Fatal(`missing service "worker"`)
	}
	if !equalStrList(worker.Command, StrList{"run-worker", "--verbose"}) {
		t.Fatalf("worker.Command = %v", worker.Command)
	}
	if worker.Environment["KEY1"] != "value1" || worker.Environment["KEY2"] != "value2" {
		t.Fatalf("worker.Environment = %v", worker.Environment)
	}

	net, ok := f.Networks["front"]
	if !ok {
		t.Fatal(`missing network "front"`)
	}
	if !net.External || net.Name != "front-net" {
		t.Fatalf("front network = %+v", net)
	}

	secret, ok := f.Secrets["db_password"]
	if !ok {
		t.Fatal(`missing secret "db_password"`)
	}
	if !secret.External || secret.Name != "db_password" {
		t.Fatalf("db_password secret = %+v", secret)
	}

	cfg, ok := f.Configs["app_config"]
	if !ok {
		t.Fatal(`missing config "app_config"`)
	}
	if !cfg.External || cfg.Name != "app_config" {
		t.Fatalf("app_config config = %+v", cfg)
	}
}

func TestParse_EmptyServices(t *testing.T) {
	_, err := Parse([]byte(`services: {}`))
	if err == nil {
		t.Fatal("expected error for empty services, got nil")
	}
	if want := "compose file defines no services"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestParse_NoServicesKey(t *testing.T) {
	_, err := Parse([]byte(`version: "3.8"`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse([]byte("services: [this is not a mapping"))
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// wrapper structs let us drive EnvMap/StrList's UnmarshalYAML directly with
// yaml.Unmarshal, including the "absent key" (node kind 0) case that never
// occurs when parsing a real, non-empty compose file.
type envWrapper struct {
	Env EnvMap `yaml:"env"`
}

type strListWrapper struct {
	List StrList `yaml:"list"`
}

func TestEnvMap_UnmarshalYAML(t *testing.T) {
	t.Run("mapping form", func(t *testing.T) {
		var w envWrapper
		if err := yaml.Unmarshal([]byte("env:\n  A: 1\n  B: 2\n"), &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if w.Env["A"] != "1" || w.Env["B"] != "2" {
			t.Fatalf("Env = %v", w.Env)
		}
	})

	t.Run("list form", func(t *testing.T) {
		var w envWrapper
		if err := yaml.Unmarshal([]byte("env:\n  - A=1\n  - B=2\n"), &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if w.Env["A"] != "1" || w.Env["B"] != "2" {
			t.Fatalf("Env = %v", w.Env)
		}
	})

	t.Run("list entry without equals sign maps to empty value", func(t *testing.T) {
		var w envWrapper
		if err := yaml.Unmarshal([]byte("env:\n  - NOEQUALS\n"), &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		v, ok := w.Env["NOEQUALS"]
		if !ok || v != "" {
			t.Fatalf("Env = %v, want {NOEQUALS: \"\"}", w.Env)
		}
	})

	t.Run("absent key leaves nil", func(t *testing.T) {
		var w envWrapper
		if err := yaml.Unmarshal([]byte("other: value\n"), &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if w.Env != nil {
			t.Fatalf("Env = %v, want nil", w.Env)
		}
	})

	t.Run("unsupported node kind errors", func(t *testing.T) {
		var w envWrapper
		err := yaml.Unmarshal([]byte("env: scalar-value\n"), &w)
		if err == nil {
			t.Fatal("expected error for scalar node, got nil")
		}
	})
}

func TestStrList_UnmarshalYAML(t *testing.T) {
	t.Run("scalar form splits on whitespace", func(t *testing.T) {
		var w strListWrapper
		if err := yaml.Unmarshal([]byte("list: run-worker --verbose\n"), &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !equalStrList(w.List, StrList{"run-worker", "--verbose"}) {
			t.Fatalf("List = %v", w.List)
		}
	})

	t.Run("sequence form", func(t *testing.T) {
		var w strListWrapper
		if err := yaml.Unmarshal([]byte("list:\n  - a\n  - b\n"), &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !equalStrList(w.List, StrList{"a", "b"}) {
			t.Fatalf("List = %v", w.List)
		}
	})

	t.Run("absent key leaves nil", func(t *testing.T) {
		var w strListWrapper
		if err := yaml.Unmarshal([]byte("other: value\n"), &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if w.List != nil {
			t.Fatalf("List = %v, want nil", w.List)
		}
	})

	t.Run("unsupported node kind errors", func(t *testing.T) {
		var w strListWrapper
		err := yaml.Unmarshal([]byte("list:\n  k: v\n"), &w)
		if err == nil {
			t.Fatal("expected error for mapping node, got nil")
		}
	})
}

func equalStrList(a, b StrList) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
