package admin

import (
	"context"
	"sort"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
)

const stackLabel = "com.docker.stack.namespace"

func (s *Server) listNodes(ctx context.Context) ([]swarm.Node, error) {
	nodes, err := s.docker.NodeList(ctx, swarm.NodeListOptions{})
	if err != nil {
		return nil, err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Description.Hostname < nodes[j].Description.Hostname })
	return nodes, nil
}

func (s *Server) listServices(ctx context.Context) ([]swarm.Service, error) {
	services, err := s.docker.ServiceList(ctx, swarm.ServiceListOptions{Status: true})
	if err != nil {
		return nil, err
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Spec.Name < services[j].Spec.Name })
	return services, nil
}

func (s *Server) listTasksForService(ctx context.Context, serviceID string) ([]swarm.Task, error) {
	tasks, err := s.docker.TaskList(ctx, swarm.TaskListOptions{
		Filters: filters.NewArgs(filters.Arg("service", serviceID)),
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Slot < tasks[j].Slot })
	return tasks, nil
}

func (s *Server) getService(ctx context.Context, id string) (swarm.Service, error) {
	svc, _, err := s.docker.ServiceInspectWithRaw(ctx, id, swarm.ServiceInspectOptions{})
	return svc, err
}

// StackSummary aggregates services that share a com.docker.stack.namespace
// label - Docker itself has no first-class "stack" object.
type StackSummary struct {
	Name     string
	Services []swarm.Service
}

func stackName(svc swarm.Service) string {
	if n, ok := svc.Spec.Labels[stackLabel]; ok && n != "" {
		return n
	}
	return ""
}

func groupByStack(services []swarm.Service) []StackSummary {
	byName := map[string][]swarm.Service{}
	for _, svc := range services {
		name := stackName(svc)
		if name == "" {
			continue
		}
		byName[name] = append(byName[name], svc)
	}
	out := make([]StackSummary, 0, len(byName))
	for name, svcs := range byName {
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Spec.Name < svcs[j].Spec.Name })
		out = append(out, StackSummary{Name: name, Services: svcs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// standaloneServices returns services that don't belong to any stack.
func standaloneServices(services []swarm.Service) []swarm.Service {
	var out []swarm.Service
	for _, svc := range services {
		if stackName(svc) == "" {
			out = append(out, svc)
		}
	}
	return out
}
