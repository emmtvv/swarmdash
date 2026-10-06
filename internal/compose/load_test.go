package compose

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoad_LongSyntax(t *testing.T) {
	data := []byte(`
services:
  app:
    image: app:1
    entrypoint: /docker-entrypoint.sh
    command: sh -c "echo 'hello world' && sleep 1"
    user: "1000:1000"
    working_dir: /srv
    stop_grace_period: 30s
    init: true
    cap_add: [NET_ADMIN]
    ulimits:
      nofile: {soft: 1024, hard: 2048}
      nproc: 512
    extra_hosts:
      db.local: 10.0.0.5
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost/"]
      interval: 10s
      retries: 3
    ports:
      - target: 80
        published: 8080
        mode: host
      - "9000-9001:9000-9001/udp"
    volumes:
      - type: tmpfs
        target: /tmp
        tmpfs: {size: 64m}
      - data:/data:ro,nocopy
      - /etc/localtime:/etc/localtime:ro
    networks:
      front:
        aliases: [web]
    secrets:
      - source: db_pw
        target: /run/secrets/password
        uid: "100"
        mode: 0400
    configs:
      - nginx_conf
    deploy:
      endpoint_mode: dnsrr
      placement:
        max_replicas_per_node: 2
      rollback_config:
        parallelism: 2
        monitor: 5s
      restart_policy:
        condition: on-failure
        max_attempts: 3
volumes:
  data:
    driver: local
    driver_opts:
      type: nfs
      o: addr=10.0.0.1,rw
      device: ":/export"
networks:
  front: {}
secrets:
  db_pw:
    external: true
configs:
  nginx_conf:
    content: |
      server {}
`)
	f, warnings, err := Load(data, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	app := f.Services["app"]

	if !reflect.DeepEqual([]string(app.Entrypoint), []string{"/docker-entrypoint.sh"}) {
		t.Errorf("Entrypoint = %q", app.Entrypoint)
	}
	if want := []string{"sh", "-c", "echo 'hello world' && sleep 1"}; !reflect.DeepEqual([]string(app.Command), want) {
		t.Errorf("Command = %q, want %q", app.Command, want)
	}
	if app.User != "1000:1000" || app.WorkingDir != "/srv" || app.StopGracePeriod != "30s" || app.Init == nil || !*app.Init {
		t.Errorf("container fields = %+v", app)
	}
	if app.Ulimits["nofile"] != (Ulimit{Soft: 1024, Hard: 2048}) || app.Ulimits["nproc"] != (Ulimit{Soft: 512, Hard: 512}) {
		t.Errorf("Ulimits = %+v", app.Ulimits)
	}
	if !reflect.DeepEqual([]string(app.ExtraHosts), []string{"db.local:10.0.0.5"}) {
		t.Errorf("ExtraHosts = %v", app.ExtraHosts)
	}
	if app.Healthcheck == nil || app.Healthcheck.Test[0] != "CMD" || app.Healthcheck.Retries == nil || *app.Healthcheck.Retries != 3 {
		t.Errorf("Healthcheck = %+v", app.Healthcheck)
	}

	wantPorts := PortList{
		{Target: 80, Published: 8080, Mode: "host"},
		{Target: 9000, Published: 9000, Protocol: "udp"},
		{Target: 9001, Published: 9001, Protocol: "udp"},
	}
	if !reflect.DeepEqual(app.Ports, wantPorts) {
		t.Errorf("Ports = %+v, want %+v", app.Ports, wantPorts)
	}

	if len(app.Volumes) != 3 {
		t.Fatalf("Volumes = %+v", app.Volumes)
	}
	if v := app.Volumes[0]; v.Type != "tmpfs" || v.Target != "/tmp" || v.Tmpfs == nil || v.Tmpfs.Size != "64m" {
		t.Errorf("tmpfs volume = %+v", v)
	}
	if v := app.Volumes[1]; v.Type != "volume" || v.Source != "data" || !v.ReadOnly || v.Volume == nil || !v.Volume.NoCopy {
		t.Errorf("named volume = %+v", v)
	}
	if v := app.Volumes[2]; v.Type != "bind" || !v.ReadOnly {
		t.Errorf("bind volume = %+v", v)
	}

	if len(app.Networks) != 1 || app.Networks[0].Name != "front" || !reflect.DeepEqual(app.Networks[0].Aliases, []string{"web"}) {
		t.Errorf("Networks = %+v", app.Networks)
	}
	if len(app.Secrets) != 1 || app.Secrets[0].Source != "db_pw" || app.Secrets[0].Target != "/run/secrets/password" || app.Secrets[0].UID != "100" || app.Secrets[0].Mode == nil || *app.Secrets[0].Mode != 0o400 {
		t.Errorf("Secrets = %+v", app.Secrets)
	}
	if len(app.Configs) != 1 || app.Configs[0].Source != "nginx_conf" {
		t.Errorf("Configs = %+v", app.Configs)
	}
	if app.Deploy.EndpointMode != "dnsrr" || app.Deploy.Placement.MaxReplicasPerNode != 2 {
		t.Errorf("Deploy = %+v", app.Deploy)
	}
	if app.Deploy.RollbackConfig == nil || app.Deploy.RollbackConfig.Parallelism != 2 || app.Deploy.RollbackConfig.Monitor != "5s" {
		t.Errorf("RollbackConfig = %+v", app.Deploy.RollbackConfig)
	}
	if rp := app.Deploy.RestartPolicy; rp == nil || rp.MaxAttempts == nil || *rp.MaxAttempts != 3 {
		t.Errorf("RestartPolicy = %+v", rp)
	}

	if v := f.Volumes["data"]; v.Driver != "local" || v.DriverOpts["type"] != "nfs" || v.DriverOpts["device"] != ":/export" {
		t.Errorf("top-level volume = %+v", v)
	}
	if c := f.Configs["nginx_conf"]; c.Content != "server {}\n" {
		t.Errorf("config content = %q", c.Content)
	}
}

func TestLoad_Warnings(t *testing.T) {
	data := []byte(`
name: proj
x-common: &common
  restart: always
services:
  web:
    <<: *common
    image: nginx
    build: .
    depends_on: [db]
    x-custom: 1
    deploy:
      replicas: 1
      replicsa: 2
  db:
    image: postgres
    env_file: .env
secrets:
  pw:
    file: ./pw.txt
`)
	_, warnings, err := Load(data, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"services.web.build", "services.web.depends_on", "services.web.deploy.replicsa", "services.db.env_file", "secrets.pw.file", "line 2: name"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"x-common", "x-custom", "<<"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("warnings should not mention %q:\n%s", unwanted, joined)
		}
	}
}

func TestLoad_Interpolation(t *testing.T) {
	data := []byte(`
services:
  web:
    image: "nginx:${TAG:-alpine}"
    environment:
      DB_HOST: ${DB_HOST}
      PRICE: "$$5"
      GREETING: "hi $USER"
      EXTRA: ${MISSING}
      OPT: ${OPT:+enabled}
    deploy:
      replicas: ${REPLICAS}
`)
	f, warnings, err := Load(data, map[string]string{"DB_HOST": "db", "REPLICAS": "3", "USER": "bob"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	web := f.Services["web"]
	if web.Image != "nginx:alpine" {
		t.Errorf("Image = %q", web.Image)
	}
	want := map[string]string{"DB_HOST": "db", "PRICE": "$5", "GREETING": "hi bob", "EXTRA": "", "OPT": ""}
	if !reflect.DeepEqual(map[string]string(web.Environment), want) {
		t.Errorf("Environment = %v, want %v", web.Environment, want)
	}
	if web.Deploy.Replicas == nil || *web.Deploy.Replicas != 3 {
		t.Errorf("Replicas = %v", web.Deploy.Replicas)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "MISSING") {
		t.Errorf("warnings = %v, want one about MISSING", warnings)
	}
}

func TestLoad_InterpolationRequired(t *testing.T) {
	_, _, err := Load([]byte("services:\n  web:\n    image: ${IMAGE:?set IMAGE first}\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "set IMAGE first") {
		t.Fatalf("err = %v, want required-variable error", err)
	}
}

func TestLoad_InterpolationNestedDefault(t *testing.T) {
	f, _, err := Load([]byte("services:\n  web:\n    image: ${IMAGE:-${REPO}:latest}\n"), map[string]string{"REPO": "me/app"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := f.Services["web"].Image; got != "me/app:latest" {
		t.Errorf("Image = %q", got)
	}
}

func TestParsePortSpec(t *testing.T) {
	cases := []struct {
		in      string
		want    []Port
		wantErr bool
	}{
		{in: "80", want: []Port{{Target: 80, Published: 80}}},
		{in: "8080:80/udp", want: []Port{{Target: 80, Published: 8080, Protocol: "udp"}}},
		{in: "127.0.0.1:8080:80", wantErr: true},
		{in: "8000-8001:80-82", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "80/icmp", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParsePortSpec(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParsePortSpec(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParsePortSpec(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
}

func TestParseVolumeSpec(t *testing.T) {
	cases := []struct {
		in      string
		want    VolumeMount
		wantErr bool
	}{
		{in: "/data", want: VolumeMount{Type: "volume", Target: "/data"}},
		{in: "./conf:/etc/app", want: VolumeMount{Type: "bind", Source: "./conf", Target: "/etc/app"}},
		{in: "/var/run/docker.sock:/var/run/docker.sock:ro", want: VolumeMount{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock", ReadOnly: true}},
		{in: "/a:/b:rshared", want: VolumeMount{Type: "bind", Source: "/a", Target: "/b", Bind: &BindOptions{Propagation: "rshared"}}},
		{in: "v:/b:bogus", wantErr: true},
		{in: "a:b:c:d", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseVolumeSpec(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseVolumeSpec(%q) = %+v, want error", c.in, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseVolumeSpec(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
}

func TestSplitShellWords(t *testing.T) {
	got, err := splitShellWords(`a "b c" 'd "e"' f\ g ""`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b c", `d "e"`, "f g", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := splitShellWords(`"open`); err == nil {
		t.Error("expected error for unterminated quote")
	}
}

func TestParseVars(t *testing.T) {
	vars, err := ParseVars("# comment\nexport A=1\nB = \"two words\"\n\nC='x'\nD=\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two words", "C": "x", "D": ""}
	if !reflect.DeepEqual(vars, want) {
		t.Errorf("vars = %v, want %v", vars, want)
	}
	if _, err := ParseVars("not a var"); err == nil {
		t.Error("expected error for line without =")
	}
	if _, err := ParseVars("1A=x"); err == nil {
		t.Error("expected error for invalid name")
	}
}
