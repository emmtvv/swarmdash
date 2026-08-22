package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/swarm"
)

type taskEventView struct {
	ID          string `json:"id"`
	Slot        int    `json:"slot"`
	NodeID      string `json:"nodeId"`
	Node        string `json:"node"`
	State       string `json:"state"`
	DesiredSt   string `json:"desiredState"`
	Message     string `json:"message"`
	Err         string `json:"err"`
	ContainerID string `json:"containerId"`
	UpdatedAt   string `json:"updatedAt"`
}

type serviceEventPayload struct {
	Replicas    uint64          `json:"replicas"`
	Running     uint64          `json:"running"`
	Desired     uint64          `json:"desired"`
	UpdateState string          `json:"updateState"`
	UpdateMsg   string          `json:"updateMessage"`
	Tasks       []taskEventView `json:"tasks"`
	FetchedAt   string          `json:"fetchedAt"`
}

// handleServiceEvents streams the live rollout/task state of a service as
// Server-Sent Events, polling the Docker API on a short interval. Docker
// doesn't push task-state changes to clients, so polling is the practical
// option here (the same approach `docker service ps` effectively uses when
// you watch it).
func (s *Server) handleServiceEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx := r.Context()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	nodes, _ := s.listNodes(ctx)
	hostnames := make(map[string]string, len(nodes))
	for _, n := range nodes {
		hostnames[n.ID] = n.Description.Hostname
	}

	for {
		svc, err := s.getService(ctx, name)
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
			flusher.Flush()
			return
		}
		tasks, err := s.listTasksForService(ctx, svc.ID)
		if err == nil {
			payload := serviceEventPayload{FetchedAt: time.Now().Format(time.RFC3339)}
			if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
				payload.Replicas = *svc.Spec.Mode.Replicated.Replicas
			}
			for _, t := range tasks {
				if t.DesiredState == swarm.TaskStateRunning {
					payload.Desired++
				}
				if t.Status.State == swarm.TaskStateRunning {
					payload.Running++
				}
			}
			if svc.UpdateStatus != nil {
				payload.UpdateState = string(svc.UpdateStatus.State)
				payload.UpdateMsg = svc.UpdateStatus.Message
			}
			for _, t := range tasks {
				containerID := ""
				if t.Status.ContainerStatus != nil {
					containerID = t.Status.ContainerStatus.ContainerID
				}
				payload.Tasks = append(payload.Tasks, taskEventView{
					ID:          t.ID,
					Slot:        t.Slot,
					NodeID:      t.NodeID,
					Node:        hostnames[t.NodeID],
					State:       string(t.Status.State),
					DesiredSt:   string(t.DesiredState),
					Message:     t.Status.Message,
					Err:         t.Status.Err,
					ContainerID: containerID,
					UpdatedAt:   t.Status.Timestamp.Format(time.RFC3339),
				})
			}
			buf, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\n", buf)
			flusher.Flush()
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
