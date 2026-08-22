package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/swarm"
)

func (s *Server) handleSwarmPage(w http.ResponseWriter, r *http.Request) {
	sw, err := s.docker.SwarmInspect(r.Context())
	if err != nil {
		http.Error(w, "inspect swarm: "+err.Error(), http.StatusBadGateway)
		return
	}
	nodes, err := s.listNodes(r.Context())
	if err != nil {
		http.Error(w, "list nodes: "+err.Error(), http.StatusBadGateway)
		return
	}
	managerAddr := ""
	for _, n := range nodes {
		if n.ManagerStatus != nil && n.ManagerStatus.Leader {
			managerAddr = n.Status.Addr
		}
	}

	services, err := s.listServices(r.Context())
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}
	replicated := 0
	for _, svc := range services {
		if svc.Spec.Mode.Replicated != nil {
			replicated++
		}
	}

	tasks, err := s.docker.TaskList(r.Context(), swarm.TaskListOptions{})
	if err != nil {
		http.Error(w, "list tasks: "+err.Error(), http.StatusBadGateway)
		return
	}
	failedTasks := 0
	for _, t := range tasks {
		if isFailedTaskState(t.Status.State) {
			failedTasks++
		}
	}

	s.render(w, r, "swarm.html", map[string]any{
		"User":                   userFromContext(r),
		"Swarm":                  sw,
		"Editor":                 swarmEditorForm(sw.Spec),
		"ManagerAddr":            managerAddr,
		"NodeCount":              len(nodes),
		"ReplicatedServiceCount": replicated,
		"FailedTaskCount":        failedTasks,
		"Flash":                  parseSwarmFlash(r),
	})
}

// swarmSpecForm holds the swarm spec's editable cluster-wide settings
// pre-rendered as plain strings, mirroring serviceEditorForm's contract
// (handlers_service_spec.go): the settings form is always pre-filled with
// the cluster's current values, so an unedited submit is a no-op.
type swarmSpecForm struct {
	Name                        string
	Labels                      string
	AutoLock                    bool
	CertExpiry                  string
	TaskHistoryRetentionLimit   string
	LogDriverName               string
	LogDriverOptions            string
	RaftSnapshotInterval        string
	RaftKeepOldSnapshots        string
	RaftLogEntriesSlowFollowers string
	RaftElectionTick            string
	RaftHeartbeatTick           string
	DispatcherHeartbeatPeriod   string
}

func swarmEditorForm(spec swarm.Spec) swarmSpecForm {
	d := swarmSpecForm{
		Name:                        spec.Name,
		Labels:                      joinKV(spec.Labels),
		AutoLock:                    spec.EncryptionConfig.AutoLockManagers,
		CertExpiry:                  spec.CAConfig.NodeCertExpiry.String(),
		RaftSnapshotInterval:        strconv.FormatUint(spec.Raft.SnapshotInterval, 10),
		RaftLogEntriesSlowFollowers: strconv.FormatUint(spec.Raft.LogEntriesForSlowFollowers, 10),
		RaftElectionTick:            strconv.Itoa(spec.Raft.ElectionTick),
		RaftHeartbeatTick:           strconv.Itoa(spec.Raft.HeartbeatTick),
		DispatcherHeartbeatPeriod:   spec.Dispatcher.HeartbeatPeriod.String(),
	}
	if spec.Orchestration.TaskHistoryRetentionLimit != nil {
		d.TaskHistoryRetentionLimit = strconv.FormatInt(*spec.Orchestration.TaskHistoryRetentionLimit, 10)
	}
	if spec.Raft.KeepOldSnapshots != nil {
		d.RaftKeepOldSnapshots = strconv.FormatUint(*spec.Raft.KeepOldSnapshots, 10)
	}
	if spec.TaskDefaults.LogDriver != nil {
		d.LogDriverName = spec.TaskDefaults.LogDriver.Name
		d.LogDriverOptions = joinKV(spec.TaskDefaults.LogDriver.Options)
	}
	return d
}

// handleSwarmUpdateSpec applies the cluster settings form: swarm name and
// labels, manager autolock, CA cert expiry, task history retention, the
// default task log driver, Raft tuning and the dispatcher heartbeat period.
// Unlike the per-action buttons below (rotate token, rebalance, prune), this
// replaces the whole editable Spec wholesale with what the form submitted -
// same wholesale-replace contract as handleServiceUpdateSpec.
func (s *Server) handleSwarmUpdateSpec(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	sw, err := s.docker.SwarmInspect(ctx)
	if err != nil {
		http.Error(w, "inspect swarm: "+err.Error(), http.StatusBadGateway)
		return
	}

	certExpiry, err := time.ParseDuration(r.FormValue("cert_expiry"))
	if err != nil {
		http.Error(w, "invalid cert expiry: "+err.Error(), http.StatusBadRequest)
		return
	}
	heartbeatPeriod, err := time.ParseDuration(r.FormValue("dispatcher_heartbeat"))
	if err != nil {
		http.Error(w, "invalid dispatcher heartbeat period: "+err.Error(), http.StatusBadRequest)
		return
	}
	snapshotInterval, err := strconv.ParseUint(r.FormValue("raft_snapshot_interval"), 10, 64)
	if err != nil {
		http.Error(w, "invalid raft snapshot interval: "+err.Error(), http.StatusBadRequest)
		return
	}
	logEntriesSlowFollowers, err := strconv.ParseUint(r.FormValue("raft_log_entries_slow_followers"), 10, 64)
	if err != nil {
		http.Error(w, "invalid raft log entries for slow followers: "+err.Error(), http.StatusBadRequest)
		return
	}
	electionTick, err := strconv.Atoi(r.FormValue("raft_election_tick"))
	if err != nil {
		http.Error(w, "invalid raft election tick: "+err.Error(), http.StatusBadRequest)
		return
	}
	heartbeatTick, err := strconv.Atoi(r.FormValue("raft_heartbeat_tick"))
	if err != nil {
		http.Error(w, "invalid raft heartbeat tick: "+err.Error(), http.StatusBadRequest)
		return
	}

	var taskHistoryLimit *int64
	if v := r.FormValue("task_history_retention"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "invalid task history retention limit: "+err.Error(), http.StatusBadRequest)
			return
		}
		taskHistoryLimit = &n
	}

	var keepOldSnapshots *uint64
	if v := r.FormValue("raft_keep_old_snapshots"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			http.Error(w, "invalid raft keep old snapshots: "+err.Error(), http.StatusBadRequest)
			return
		}
		keepOldSnapshots = &n
	}

	var logDriver *swarm.Driver
	if name := r.FormValue("log_driver_name"); name != "" {
		logDriver = &swarm.Driver{Name: name, Options: parseKVLines(r.FormValue("log_driver_options"))}
	}

	spec := sw.Spec
	spec.Name = r.FormValue("name")
	spec.Labels = parseKVLines(r.FormValue("labels"))
	wasAutoLock := spec.EncryptionConfig.AutoLockManagers
	autoLock := r.FormValue("autolock") == "on"
	spec.EncryptionConfig.AutoLockManagers = autoLock
	spec.CAConfig.NodeCertExpiry = certExpiry
	spec.Orchestration.TaskHistoryRetentionLimit = taskHistoryLimit
	spec.TaskDefaults.LogDriver = logDriver
	spec.Raft.SnapshotInterval = snapshotInterval
	spec.Raft.KeepOldSnapshots = keepOldSnapshots
	spec.Raft.LogEntriesForSlowFollowers = logEntriesSlowFollowers
	spec.Raft.ElectionTick = electionTick
	spec.Raft.HeartbeatTick = heartbeatTick
	spec.Dispatcher.HeartbeatPeriod = heartbeatPeriod

	if err := s.docker.SwarmUpdate(ctx, sw.Version, spec, swarm.UpdateFlags{}); err != nil {
		http.Error(w, "update swarm: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "swarm.update_spec", "", "", nil)

	// Autolock was just turned on: the manager TLS keys/raft data are now
	// encrypted at rest and need this key to unlock on next start. It's
	// only ever retrievable right after enabling - docker never displays
	// it again - so surface it once via the flash banner, same as the
	// join tokens above.
	if !wasAutoLock && autoLock {
		key, err := s.docker.SwarmGetUnlockKey(ctx)
		if err == nil && key.UnlockKey != "" {
			redirect(w, r, "/settings/swarm?flash=settings&unlock_key="+url.QueryEscape(key.UnlockKey))
			return
		}
	}
	redirect(w, r, "/settings/swarm?flash=settings")
}

// handleSwarmForceCertRotate forces the swarm to generate a new root CA
// certificate and key by bumping CAConfig.ForceRotate, the mechanism
// SwarmUpdate uses to trigger rotation (there's no dedicated rotate flag -
// changing this counter with no signing cert/key configured is what tells
// docker to regenerate the root cert, same as `docker swarm ca --rotate`).
func (s *Server) handleSwarmForceCertRotate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sw, err := s.docker.SwarmInspect(ctx)
	if err != nil {
		http.Error(w, "inspect swarm: "+err.Error(), http.StatusBadGateway)
		return
	}
	spec := sw.Spec
	spec.CAConfig.ForceRotate++
	if err := s.docker.SwarmUpdate(ctx, sw.Version, spec, swarm.UpdateFlags{}); err != nil {
		http.Error(w, "rotate CA cert: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "swarm.force_cert_rotate", "", "", nil)
	redirect(w, r, "/settings/swarm?flash=certrotate")
}

// handleSwarmRebalance force-updates every replicated service so Swarm
// re-places all of its tasks. Swarm only weighs current node load when
// placing *new* tasks - it never moves an already-running task onto a node
// that joined afterward - so after adding/removing nodes, existing
// replicated services can end up lopsided until something recreates their
// tasks. A force update (ForceUpdate++, same as the single-service Restart
// button - see doServiceRestart in handlers_service_actions.go) is the only
// way to make the scheduler reconsider placement for tasks that are
// already running. Global-mode services are skipped: Swarm already starts/
// stops their one-task-per-node automatically as nodes join/leave, so
// there's nothing to rebalance.
func (s *Server) handleSwarmRebalance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	services, err := s.listServices(ctx)
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}

	ok, failed := 0, 0
	for _, svc := range services {
		if svc.Spec.Mode.Replicated == nil {
			continue
		}
		name, err := s.doServiceRestart(ctx, svc.ID)
		if err != nil {
			failed++
			s.audit(r, "swarm.rebalance", name, "", err)
			continue
		}
		ok++
		s.audit(r, "swarm.rebalance", name, "", nil)
	}

	s.log.Info("swarm rebalance triggered", "services_restarted", ok, "failed", failed, "by", userFromContext(r).Username)
	redirect(w, r, "/settings/swarm")
}

func (s *Server) handleSwarmRotateToken(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	ctx := r.Context()

	sw, err := s.docker.SwarmInspect(ctx)
	if err != nil {
		http.Error(w, "inspect swarm: "+err.Error(), http.StatusBadGateway)
		return
	}

	flags := swarm.UpdateFlags{}
	switch role {
	case "worker":
		flags.RotateWorkerToken = true
	case "manager":
		flags.RotateManagerToken = true
	default:
		http.Error(w, "unknown token role", http.StatusBadRequest)
		return
	}

	if err := s.docker.SwarmUpdate(ctx, sw.Version, sw.Spec, flags); err != nil {
		http.Error(w, "rotate token: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "swarm.rotate_token", role, "", nil)
	redirect(w, r, "/settings/swarm")
}

// isFailedTaskState reports whether a task has actually failed - as opposed
// to shutdown/complete, which cover ordinary lifecycle events like a
// rolling update replacing an old replica. Only failed/rejected tasks are
// offered for cleanup below, so a routine deploy never looks like something
// that needs "fixing".
func isFailedTaskState(state swarm.TaskState) bool {
	return state == swarm.TaskStateFailed || state == swarm.TaskStateRejected
}

// swarmFlash carries a one-shot result banner across the redirect after a
// cluster maintenance action - there's no session-backed flash mechanism in
// this app, so the counts just ride along as query parameters.
type swarmFlash struct {
	Kind                                          string
	Removed, Failed                               int
	Containers, Images, Networks, Volumes, Errors int
	Reclaimed                                     int64
	UnlockKey                                     string
}

func parseSwarmFlash(r *http.Request) *swarmFlash {
	kind := r.URL.Query().Get("flash")
	if kind == "" {
		return nil
	}
	qi := func(key string) int {
		n, _ := strconv.Atoi(r.URL.Query().Get(key))
		return n
	}
	reclaimed, _ := strconv.ParseInt(r.URL.Query().Get("reclaimed"), 10, 64)
	return &swarmFlash{
		Kind:       kind,
		Removed:    qi("removed"),
		Failed:     qi("failed"),
		Containers: qi("containers"),
		Images:     qi("images"),
		Networks:   qi("networks"),
		Volumes:    qi("volumes"),
		Errors:     qi("errors"),
		Reclaimed:  reclaimed,
		UnlockKey:  r.URL.Query().Get("unlock_key"),
	}
}

// handleSwarmPruneFailedTasks removes the leftover container behind every
// failed/rejected task across the cluster. Docker Swarm has no API to
// delete a task directly - completed/failed tasks are only ever trimmed by
// dockerd's own reaper once a service's TaskHistoryRetentionLimit is
// exceeded - but the dead container underneath one is an ordinary Docker
// object, so removing it directly (via the owning node's agent) is safe:
// the container is already stopped and not serving anything, this just
// reclaims its disk footprint sooner instead of waiting on the reaper.
func (s *Server) handleSwarmPruneFailedTasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tasks, err := s.docker.TaskList(ctx, swarm.TaskListOptions{})
	if err != nil {
		http.Error(w, "list tasks: "+err.Error(), http.StatusBadGateway)
		return
	}

	removed, failed := 0, 0
	for _, t := range tasks {
		if !isFailedTaskState(t.Status.State) {
			continue
		}
		if t.NodeID == "" || t.Status.ContainerStatus == nil || t.Status.ContainerStatus.ContainerID == "" {
			continue
		}
		resp, err := s.agentPost(ctx, t.NodeID, "/v1/containers/"+t.Status.ContainerStatus.ContainerID+"/remove", nil)
		if err != nil {
			failed++
			continue
		}
		ok := resp.StatusCode == http.StatusOK
		resp.Body.Close()
		if !ok {
			failed++
			continue
		}
		removed++
	}

	s.audit(r, "swarm.prune_failed_tasks", "", fmt.Sprintf("removed=%d failed=%d", removed, failed), nil)
	redirect(w, r, fmt.Sprintf("/settings/swarm?flash=tasks&removed=%d&failed=%d", removed, failed))
}

// systemPruneReport mirrors agent.SystemPruneReport - kept as a separate
// type here rather than importing the agent package, matching how the rest
// of admin talks to agents purely over the HTTP/JSON boundary.
type systemPruneReport struct {
	ContainersDeleted []string `json:"containersDeleted"`
	NetworksDeleted   []string `json:"networksDeleted"`
	ImagesDeleted     int      `json:"imagesDeleted"`
	VolumesDeleted    []string `json:"volumesDeleted"`
	SpaceReclaimed    uint64   `json:"spaceReclaimed"`
}

// handleSwarmPruneResources runs a `docker system prune`-equivalent on
// every node's agent and sums the results. This is the cluster-wide version
// of the per-node prune buttons already on the images/volumes pages -
// useful when the goal is "free up space everywhere" rather than picking
// through nodes one at a time.
func (s *Server) handleSwarmPruneResources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	all := r.FormValue("all") == "on"
	withVolumes := r.FormValue("volumes") == "on"

	nodes, err := s.listNodes(ctx)
	if err != nil {
		http.Error(w, "list nodes: "+err.Error(), http.StatusBadGateway)
		return
	}

	q := url.Values{}
	if all {
		q.Set("all", "true")
	}
	if withVolumes {
		q.Set("volumes", "true")
	}
	path := "/v1/system/prune"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var containers, images, networks, volumes, errs int
	var reclaimed uint64
	for _, n := range nodes {
		resp, err := s.agentPost(ctx, n.ID, path, nil)
		if err != nil {
			errs++
			continue
		}
		var report systemPruneReport
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(resp.Body).Decode(&report)
		} else {
			errs++
		}
		resp.Body.Close()
		containers += len(report.ContainersDeleted)
		networks += len(report.NetworksDeleted)
		images += report.ImagesDeleted
		volumes += len(report.VolumesDeleted)
		reclaimed += report.SpaceReclaimed
	}

	s.audit(r, "swarm.prune_resources", "",
		fmt.Sprintf("all=%v volumes=%v containers=%d images=%d networks=%d volumes=%d reclaimed=%d errors=%d",
			all, withVolumes, containers, images, networks, volumes, reclaimed, errs), nil)
	redirect(w, r, fmt.Sprintf("/settings/swarm?flash=resources&containers=%d&images=%d&networks=%d&volumes=%d&reclaimed=%d&errors=%d",
		containers, images, networks, volumes, reclaimed, errs))
}
