package admin

import (
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/swarm"
	"gopkg.in/yaml.v3"

	"swarmdash/internal/compose"
)

func TestCpusToString(t *testing.T) {
	tests := []struct {
		nano int64
		want string
	}{
		{0, ""},
		{1_000_000_000, "1"},
		{1_500_000_000, "1.5"},
		{250_000_000, "0.25"},
	}
	for _, tt := range tests {
		if got := cpusToString(tt.nano); got != tt.want {
			t.Errorf("cpusToString(%d) = %q, want %q", tt.nano, got, tt.want)
		}
	}
}

func TestBytesToString(t *testing.T) {
	tests := []struct {
		b    int64
		want string
	}{
		{0, ""},
		{268435456, "268435456"},
	}
	for _, tt := range tests {
		if got := bytesToString(tt.b); got != tt.want {
			t.Errorf("bytesToString(%d) = %q, want %q", tt.b, got, tt.want)
		}
	}
}

func fullReplicatedService() swarm.Service {
	replicas := uint64(3)
	parallelism := uint64(1)
	svc := swarm.Service{
		Spec: swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{
					Image:   "nginx:alpine",
					Command: []string{"nginx", "-g", "daemon off;"},
					Env:     []string{"FOO=bar", "BAZ=qux"},
					Labels:  map[string]string{"app": "web"},
					Mounts: []mount.Mount{
						{Source: "data", Target: "/var/www", ReadOnly: true},
						{Source: "/host/cache", Target: "/cache"},
					},
					Secrets: []*swarm.SecretReference{
						{SecretName: "db_password"},
					},
					Configs: []*swarm.ConfigReference{
						{ConfigName: "app_config"},
					},
				},
				Networks: []swarm.NetworkAttachmentConfig{
					{Target: "front"},
					{Target: "back"},
				},
				Placement: &swarm.Placement{
					Constraints: []string{"node.role==worker"},
					Preferences: []swarm.PlacementPreference{
						{Spread: &swarm.SpreadOver{SpreadDescriptor: "node.labels.zone"}},
					},
				},
				Resources: &swarm.ResourceRequirements{
					Limits:       &swarm.Limit{NanoCPUs: 1_000_000_000, MemoryBytes: 536870912},
					Reservations: &swarm.Resources{NanoCPUs: 500_000_000, MemoryBytes: 268435456},
				},
				RestartPolicy: &swarm.RestartPolicy{Condition: swarm.RestartPolicyConditionOnFailure},
			},
			Mode: swarm.ServiceMode{
				Replicated: &swarm.ReplicatedService{Replicas: &replicas},
			},
			UpdateConfig: &swarm.UpdateConfig{
				Parallelism:   parallelism,
				Order:         "start-first",
				FailureAction: "rollback",
			},
		},
		Endpoint: swarm.Endpoint{
			Spec: swarm.EndpointSpec{
				Ports: []swarm.PortConfig{
					{PublishedPort: 8080, TargetPort: 80, Protocol: swarm.PortConfigProtocolTCP},
					{PublishedPort: 9000, TargetPort: 9000, Protocol: swarm.PortConfigProtocolUDP},
				},
			},
		},
	}
	return svc
}

func TestExportService_Replicated(t *testing.T) {
	svc := fullReplicatedService()
	out := exportService(svc)

	if out.Image != "nginx:alpine" {
		t.Errorf("Image = %q", out.Image)
	}
	// ContainerSpec.Command is the image ENTRYPOINT override; compose's
	// command: maps to Args.
	wantEntrypoint := []string{"nginx", "-g", "daemon off;"}
	if !reflect.DeepEqual([]string(out.Entrypoint), wantEntrypoint) {
		t.Errorf("Entrypoint = %v, want %v", out.Entrypoint, wantEntrypoint)
	}
	if len(out.Command) != 0 {
		t.Errorf("Command = %v, want none", out.Command)
	}
	if out.Environment["FOO"] != "bar" || out.Environment["BAZ"] != "qux" {
		t.Errorf("Environment = %v", out.Environment)
	}
	if out.Labels["app"] != "web" {
		t.Errorf("Labels = %v", out.Labels)
	}

	wantVolumes := []compose.VolumeMount{
		{Type: "volume", Source: "data", Target: "/var/www", ReadOnly: true},
		{Type: "bind", Source: "/host/cache", Target: "/cache"},
	}
	if !reflect.DeepEqual(out.Volumes, wantVolumes) {
		t.Errorf("Volumes = %+v, want %+v", out.Volumes, wantVolumes)
	}

	if len(out.Secrets) != 1 || out.Secrets[0] != (compose.FileRef{Source: "db_password"}) {
		t.Errorf("Secrets = %v", out.Secrets)
	}
	if len(out.Configs) != 1 || out.Configs[0] != (compose.FileRef{Source: "app_config"}) {
		t.Errorf("Configs = %v", out.Configs)
	}

	wantPorts := compose.PortList{{Target: 80, Published: 8080}, {Target: 9000, Published: 9000, Protocol: "udp"}}
	if !reflect.DeepEqual(out.Ports, wantPorts) {
		t.Errorf("Ports = %v, want %v", out.Ports, wantPorts)
	}

	if got := out.Networks.Names(); !reflect.DeepEqual(got, []string{"front", "back"}) {
		t.Errorf("Networks = %v", got)
	}

	if out.Deploy.Mode != "" {
		t.Errorf("Deploy.Mode = %q, want empty for replicated", out.Deploy.Mode)
	}
	if out.Deploy.Replicas == nil || *out.Deploy.Replicas != 3 {
		t.Errorf("Deploy.Replicas = %v, want 3", out.Deploy.Replicas)
	}

	if len(out.Deploy.Placement.Constraints) != 1 || out.Deploy.Placement.Constraints[0] != "node.role==worker" {
		t.Errorf("Deploy.Placement.Constraints = %v", out.Deploy.Placement.Constraints)
	}
	if len(out.Deploy.Placement.Preferences) != 1 || out.Deploy.Placement.Preferences[0].Spread != "node.labels.zone" {
		t.Errorf("Deploy.Placement.Preferences = %v", out.Deploy.Placement.Preferences)
	}

	if out.Deploy.Resources.Limits == nil || out.Deploy.Resources.Limits.CPUs != "1" || out.Deploy.Resources.Limits.Memory != "536870912" {
		t.Errorf("Deploy.Resources.Limits = %+v", out.Deploy.Resources.Limits)
	}
	if out.Deploy.Resources.Reservations == nil || out.Deploy.Resources.Reservations.CPUs != "0.5" || out.Deploy.Resources.Reservations.Memory != "268435456" {
		t.Errorf("Deploy.Resources.Reservations = %+v", out.Deploy.Resources.Reservations)
	}

	if out.Deploy.UpdateConfig == nil {
		t.Fatal("Deploy.UpdateConfig is nil")
	}
	if out.Deploy.UpdateConfig.Parallelism != 1 || out.Deploy.UpdateConfig.Order != "start-first" || out.Deploy.UpdateConfig.FailureAction != "rollback" {
		t.Errorf("Deploy.UpdateConfig = %+v", out.Deploy.UpdateConfig)
	}

	if out.Deploy.RestartPolicy == nil || out.Deploy.RestartPolicy.Condition != "on-failure" {
		t.Errorf("Deploy.RestartPolicy = %+v", out.Deploy.RestartPolicy)
	}
}

func TestExportService_Global(t *testing.T) {
	svc := swarm.Service{
		Spec: swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{Image: "fluentd:latest"},
			},
			Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}},
		},
	}
	out := exportService(svc)
	if out.Deploy.Mode != "global" {
		t.Errorf("Deploy.Mode = %q, want global", out.Deploy.Mode)
	}
	if out.Deploy.Replicas != nil {
		t.Errorf("Deploy.Replicas = %v, want nil for global mode", out.Deploy.Replicas)
	}
}

func TestExportService_PortDefaultProtocolOmitted(t *testing.T) {
	svc := swarm.Service{
		Endpoint: swarm.Endpoint{
			Spec: swarm.EndpointSpec{
				Ports: []swarm.PortConfig{
					{PublishedPort: 80, TargetPort: 80},
				},
			},
		},
	}
	out := exportService(svc)
	if len(out.Ports) != 1 || out.Ports[0].String() != "80:80" {
		t.Errorf("Ports = %v, want [80:80]", out.Ports)
	}
}

func TestExportComposeYAML_RoundTrips(t *testing.T) {
	svc := fullReplicatedService()
	exported := exportService(svc)

	data, err := exportComposeYAML(&compose.File{Services: map[string]compose.Service{"web": exported}})
	if err != nil {
		t.Fatalf("exportComposeYAML: %v", err)
	}

	// Must be valid, generic YAML.
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		t.Fatalf("exported YAML did not parse: %v\n%s", err, data)
	}

	// Must also round-trip through our own Parse.
	parsed, err := compose.Parse(data)
	if err != nil {
		t.Fatalf("compose.Parse(exported YAML): %v\n%s", err, data)
	}
	web, ok := parsed.Services["web"]
	if !ok {
		t.Fatalf("parsed file missing service %q:\n%s", "web", data)
	}
	if web.Image != exported.Image {
		t.Errorf("round-tripped Image = %q, want %q", web.Image, exported.Image)
	}
	if len(web.Environment) != len(exported.Environment) {
		t.Errorf("round-tripped Environment = %v, want %v", web.Environment, exported.Environment)
	}
	if web.Deploy.Replicas == nil || exported.Deploy.Replicas == nil || *web.Deploy.Replicas != *exported.Deploy.Replicas {
		t.Errorf("round-tripped Deploy.Replicas = %v, want %v", web.Deploy.Replicas, exported.Deploy.Replicas)
	}
}
