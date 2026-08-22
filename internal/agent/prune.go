package agent

import (
	"net/http"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"swarmdash/internal/httpx"
)

// SystemPruneReport aggregates the per-resource prune reports dockerd
// returns separately, so admin only needs one round trip per node.
type SystemPruneReport struct {
	ContainersDeleted []string `json:"containersDeleted"`
	NetworksDeleted   []string `json:"networksDeleted"`
	ImagesDeleted     int      `json:"imagesDeleted"`
	VolumesDeleted    []string `json:"volumesDeleted"`
	SpaceReclaimed    uint64   `json:"spaceReclaimed"`
}

// handleSystemPrune runs the same sequence as `docker system prune`:
// stopped containers, then unused networks, then dangling (or with
// ?all=true, all unused) images. Nodes in a swarm normally only run
// swarm-managed containers, so - like the CLI equivalent - this isn't
// scoped to any particular label; anything already stopped is fair game.
// Unused volumes are only touched with ?volumes=true since, unlike the
// others, a volume can hold data nothing currently references but that's
// still wanted.
func (s *Server) handleSystemPrune(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	all := r.URL.Query().Get("all") == "true"
	withVolumes := r.URL.Query().Get("volumes") == "true"

	var report SystemPruneReport

	cReport, err := s.docker.ContainersPrune(ctx, filters.NewArgs())
	if err != nil {
		http.Error(w, "prune containers: "+err.Error(), http.StatusBadGateway)
		return
	}
	report.ContainersDeleted = cReport.ContainersDeleted
	report.SpaceReclaimed += cReport.SpaceReclaimed

	nReport, err := s.docker.NetworksPrune(ctx, filters.NewArgs())
	if err != nil {
		http.Error(w, "prune networks: "+err.Error(), http.StatusBadGateway)
		return
	}
	report.NetworksDeleted = nReport.NetworksDeleted

	dangling := "true"
	if all {
		dangling = "false"
	}
	iReport, err := s.docker.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", dangling)))
	if err != nil {
		http.Error(w, "prune images: "+err.Error(), http.StatusBadGateway)
		return
	}
	report.ImagesDeleted = len(iReport.ImagesDeleted)
	report.SpaceReclaimed += iReport.SpaceReclaimed

	if withVolumes {
		vReport, err := s.docker.VolumesPrune(ctx, filters.NewArgs())
		if err != nil {
			http.Error(w, "prune volumes: "+err.Error(), http.StatusBadGateway)
			return
		}
		report.VolumesDeleted = vReport.VolumesDeleted
		report.SpaceReclaimed += vReport.SpaceReclaimed
	}

	httpx.WriteJSON(w, report)
}

// handleContainerRemove force-removes a single container by ID. Admin uses
// this to clean up the leftover container behind a terminated (failed/
// rejected) swarm task - dockerd otherwise keeps it around until
// TaskHistoryRetentionLimit trims it, which on a cluster with a
// crash-looping service can mean a lot of dead weight sitting on disk.
func (s *Server) handleContainerRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.ContainerRemove(r.Context(), id, container.RemoveOptions{Force: true}); err != nil {
		http.Error(w, "remove container: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}
