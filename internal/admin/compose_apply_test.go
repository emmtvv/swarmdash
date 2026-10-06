package admin

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/compose"
)

func TestBuildServiceSpec_ComposeFields(t *testing.T) {
	file, warnings, err := compose.Load([]byte(`
services:
  app:
    image: me/app:1
    entrypoint: ["/init"]
    command: ["serve"]
    environment: {B: "2", A: "1"}
    user: "1000"
    working_dir: /srv
    stop_grace_period: 20s
    extra_hosts: ["db.local:10.0.0.5"]
    ulimits: {nofile: 1024}
    healthcheck:
      test: curl -f localhost
      interval: 5s
      retries: 2
    logging: {driver: json-file, options: {max-size: 10m}}
    ports:
      - {target: 80, published: 8080, mode: host}
    volumes:
      - data:/data
      - type: tmpfs
        target: /tmp
        tmpfs: {size: 1m}
    networks:
      front: {aliases: [api]}
    secrets:
      - db_pw
      - {source: tls, target: /certs/key.pem, mode: 0400}
    deploy:
      endpoint_mode: dnsrr
      rollback_config: {parallelism: 2, monitor: 3s}
      restart_policy: {condition: on-failure, max_attempts: 5, delay: 2s}
      resources: {limits: {pids: 100}}
volumes:
  data:
    driver: local
    driver_opts: {type: nfs, device: ":/export"}
networks:
  front: {}
secrets:
  db_pw: {external: true, name: db_pw_v3}
  tls: {external: true}
`), nil)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load: %v %v", err, warnings)
	}
	env := composeEnv{
		stack:    "shop",
		networks: map[string]string{"front": "shop_front"},
		secrets:  map[string]resolvedRef{"db_pw": {ID: "s1", Name: "db_pw_v3"}, "tls": {ID: "s2", Name: "tls"}},
		volumes:  file.Volumes,
	}
	spec, err := buildServiceSpec("shop_app", file.Services["app"], env)
	if err != nil {
		t.Fatalf("buildServiceSpec: %v", err)
	}
	cs := spec.TaskTemplate.ContainerSpec

	if !reflect.DeepEqual(cs.Command, []string{"/init"}) || !reflect.DeepEqual(cs.Args, []string{"serve"}) {
		t.Errorf("entrypoint/command = %v / %v", cs.Command, cs.Args)
	}
	if !reflect.DeepEqual(cs.Env, []string{"A=1", "B=2"}) {
		t.Errorf("Env = %v, want sorted", cs.Env)
	}
	if cs.User != "1000" || cs.Dir != "/srv" || cs.StopGracePeriod == nil || *cs.StopGracePeriod != 20*time.Second {
		t.Errorf("user/dir/grace = %q %q %v", cs.User, cs.Dir, cs.StopGracePeriod)
	}
	if !reflect.DeepEqual(cs.Hosts, []string{"10.0.0.5 db.local"}) {
		t.Errorf("Hosts = %v", cs.Hosts)
	}
	if len(cs.Ulimits) != 1 || cs.Ulimits[0].Name != "nofile" || cs.Ulimits[0].Hard != 1024 {
		t.Errorf("Ulimits = %+v", cs.Ulimits)
	}
	if hc := cs.Healthcheck; hc == nil || !reflect.DeepEqual(hc.Test, []string{"CMD-SHELL", "curl -f localhost"}) || hc.Interval != 5*time.Second || hc.Retries != 2 {
		t.Errorf("Healthcheck = %+v", cs.Healthcheck)
	}
	if ld := spec.TaskTemplate.LogDriver; ld == nil || ld.Name != "json-file" || ld.Options["max-size"] != "10m" {
		t.Errorf("LogDriver = %+v", ld)
	}
	if p := spec.EndpointSpec; p.Mode != swarm.ResolutionModeDNSRR || p.Ports[0].PublishMode != swarm.PortConfigPublishModeHost || p.Ports[0].PublishedPort != 8080 {
		t.Errorf("EndpointSpec = %+v", p)
	}

	data := cs.Mounts[0]
	if data.Type != mount.TypeVolume || data.Source != "data" || data.VolumeOptions == nil || data.VolumeOptions.DriverConfig.Options["type"] != "nfs" {
		t.Errorf("volume mount = %+v", data)
	}
	if tmp := cs.Mounts[1]; tmp.Type != mount.TypeTmpfs || tmp.TmpfsOptions.SizeBytes != 1<<20 {
		t.Errorf("tmpfs mount = %+v", tmp)
	}

	if na := spec.TaskTemplate.Networks; len(na) != 1 || na[0].Target != "shop_front" || !reflect.DeepEqual(na[0].Aliases, []string{"api"}) {
		t.Errorf("Networks = %+v", na)
	}

	// The default target is the compose key, not the (rotated) secret name.
	if ref := cs.Secrets[0]; ref.SecretName != "db_pw_v3" || ref.File.Name != "db_pw" || ref.File.Mode != 0o444 {
		t.Errorf("secret[0] = %+v %+v", ref, ref.File)
	}
	if ref := cs.Secrets[1]; ref.File.Name != "/certs/key.pem" || ref.File.Mode != 0o400 {
		t.Errorf("secret[1] file = %+v", ref.File)
	}

	if rc := spec.RollbackConfig; rc == nil || rc.Parallelism != 2 || rc.Monitor != 3*time.Second {
		t.Errorf("RollbackConfig = %+v", rc)
	}
	if rp := spec.TaskTemplate.RestartPolicy; rp == nil || *rp.MaxAttempts != 5 || *rp.Delay != 2*time.Second {
		t.Errorf("RestartPolicy = %+v", rp)
	}
	if spec.TaskTemplate.Resources.Limits.Pids != 100 {
		t.Errorf("pids limit = %d", spec.TaskTemplate.Resources.Limits.Pids)
	}
}

func TestBuildServiceSpec_Errors(t *testing.T) {
	cases := map[string]string{
		"undeclared secret":   "services:\n  a:\n    image: x\n    secrets: [nope]\n",
		"undeclared network":  "services:\n  a:\n    image: x\n    networks: [nope]\n",
		"bad mode":            "services:\n  a:\n    image: x\n    deploy: {mode: sideways}\n",
		"bad duration":        "services:\n  a:\n    image: x\n    stop_grace_period: soon\n",
		"bad restart":         "services:\n  a:\n    image: x\n    deploy: {restart_policy: {condition: sometimes}}\n",
		"missing image":       "services:\n  a:\n    command: x\n",
		"bad endpoint mode":   "services:\n  a:\n    image: x\n    deploy: {endpoint_mode: magic}\n",
		"bad extra host form": "services:\n  a:\n    image: x\n    extra_hosts: [justahost]\n",
	}
	for name, yml := range cases {
		file, _, err := compose.Load([]byte(yml), nil)
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if _, err := buildServiceSpec("s_a", file.Services["a"], composeEnv{stack: "s"}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestInlineConfigName(t *testing.T) {
	a := inlineConfigName("shop", "nginx", "server {}")
	b := inlineConfigName("shop", "nginx", "server { }")
	if a == b || !strings.HasPrefix(a, "shop_nginx_") {
		t.Errorf("names = %q / %q - must be content-addressed", a, b)
	}
	long := inlineConfigName(strings.Repeat("s", 40), strings.Repeat("k", 40), "x")
	if len(long) > 64 {
		t.Errorf("name %q exceeds swarm's 64-char limit", long)
	}
}

func TestEnsureComposeConfigs_InlineContent(t *testing.T) {
	fs := newFakeSwarm()
	s := newTestServer(t, fs.client(t))
	defs := map[string]compose.ConfigDef{"conf": {Content: "hello"}}

	dry, err := s.ensureComposeConfigs(t.Context(), "app", defs, true)
	if err != nil || dry["conf"].ID != "" || len(fs.configs) != 0 {
		t.Fatalf("dry run must not create: %+v %v (configs=%d)", dry, err, len(fs.configs))
	}
	got, err := s.ensureComposeConfigs(t.Context(), "app", defs, false)
	if err != nil || got["conf"].ID == "" || len(fs.configs) != 1 {
		t.Fatalf("create: %+v %v", got, err)
	}
	again, _ := s.ensureComposeConfigs(t.Context(), "app", defs, false)
	if again["conf"].ID != got["conf"].ID || len(fs.configs) != 1 {
		t.Errorf("same content should reuse the existing config")
	}
}
