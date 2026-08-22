package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"syscall"

	"github.com/docker/docker/api/types/container"
	"github.com/gorilla/websocket"

	"swarmdash/internal/httpx"
)

// handleStats streams the raw newline-delimited JSON produced by the Docker
// stats API to the caller, one websocket text message per line.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	reader, err := s.docker.ContainerStats(r.Context(), id, true)
	if err != nil {
		http.Error(w, "stats: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer reader.Body.Close()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	sc := bufio.NewScanner(reader.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if err := conn.WriteMessage(websocket.TextMessage, sc.Bytes()); err != nil {
			return
		}
	}
}

// statsSummary is this node's aggregate real resource usage, as opposed to
// what services merely declare via deploy.resources.reservations.*.
type statsSummary struct {
	MemUsedBytes   int64   `json:"mem_used_bytes"`
	CPUUsedCores   float64 `json:"cpu_used_cores"`
	DiskTotalBytes int64   `json:"disk_total_bytes"`
	DiskUsedBytes  int64   `json:"disk_used_bytes"`
	Containers     int     `json:"containers"`
}

// handleStatsSummary reports total non-cache memory usage, CPU usage (in
// fractional cores), and real node disk usage, so admin can show real
// per-node - and aggregate cluster-wide - usage without opening a
// streaming connection per container itself.
func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	containers, err := s.docker.ContainerList(r.Context(), container.ListOptions{})
	if err != nil {
		http.Error(w, "list containers: "+err.Error(), http.StatusBadGateway)
		return
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		memTotal int64
		cpuTotal float64
	)
	for _, c := range containers {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			mem, cpu, err := s.containerResourceUsage(r.Context(), id)
			if err != nil {
				return
			}
			mu.Lock()
			memTotal += mem
			cpuTotal += cpu
			mu.Unlock()
		}(c.ID)
	}
	wg.Wait()

	diskTotal, diskUsed, err := s.diskUsage()
	if err != nil {
		s.log.Warn("disk usage", "err", err)
	}

	httpx.WriteJSON(w, statsSummary{
		MemUsedBytes:   memTotal,
		CPUUsedCores:   cpuTotal,
		DiskTotalBytes: diskTotal,
		DiskUsedBytes:  diskUsed,
		Containers:     len(containers),
	})
}

// diskUsage reports total and used space on the node's real disk. The agent
// itself runs as a container with nothing but the Docker socket mounted in
// (see deploy/stack.yml) - it has no bind mount of the host's filesystem,
// so statting info.DockerRootDir (e.g. /var/lib/docker) from in here would
// just fail, since that path doesn't exist inside the agent's own minimal
// image. Statting "/" instead works without any extra mount: with the
// default overlay2 storage driver, a container's root is an overlay mount
// whose free-space figures the kernel reports from the *upper* filesystem -
// the host filesystem backing /var/lib/docker/overlay2 - which is exactly
// the real, node-local disk pressure we want (see overlayfs(5), "df").
func (s *Server) diskUsage() (total, used int64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0, fmt.Errorf("statfs /: %w", err)
	}
	total = int64(uint64(stat.Bsize) * stat.Blocks)
	free := int64(uint64(stat.Bsize) * stat.Bavail)
	return total, total - free, nil
}

// containerResourceUsage reads two consecutive samples off a container's
// streaming stats feed (Docker emits one roughly every second) rather than
// a single one-shot sample, because CPU usage is only meaningful as a rate:
// it's derived from the delta between two cumulative counters, the same way
// `docker stats` computes its percentage column. Memory is read off the
// second sample with the page cache excluded, matching what `docker stats`
// shows - raw cgroup usage includes reclaimable page cache, which would
// make idle containers look like they're using far more memory than they
// actually need.
func (s *Server) containerResourceUsage(ctx context.Context, id string) (memUsed int64, cpuCores float64, err error) {
	resp, err := s.docker.ContainerStats(ctx, id, true)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	var first, second container.StatsResponse
	if !sc.Scan() {
		return 0, 0, fmt.Errorf("container %s: no stats sample (stream closed: %v)", id, sc.Err())
	}
	if err := json.Unmarshal(sc.Bytes(), &first); err != nil {
		return 0, 0, err
	}
	if !sc.Scan() {
		return 0, 0, fmt.Errorf("container %s: only one stats sample (stream closed: %v)", id, sc.Err())
	}
	if err := json.Unmarshal(sc.Bytes(), &second); err != nil {
		return 0, 0, err
	}

	cache := second.MemoryStats.Stats["cache"]
	if cache == 0 {
		cache = second.MemoryStats.Stats["inactive_file"] // cgroup v2
	}
	memUsed = int64(second.MemoryStats.Usage) - int64(cache)
	if memUsed < 0 {
		memUsed = 0
	}

	cpuDelta := float64(second.CPUStats.CPUUsage.TotalUsage) - float64(first.CPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(second.CPUStats.SystemUsage) - float64(first.CPUStats.SystemUsage)
	onlineCPUs := float64(second.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(second.CPUStats.CPUUsage.PercpuUsage))
	}
	if systemDelta > 0 && cpuDelta > 0 {
		cpuCores = (cpuDelta / systemDelta) * onlineCPUs
	}
	return memUsed, cpuCores, nil
}
