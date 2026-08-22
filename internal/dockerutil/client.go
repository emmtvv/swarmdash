// Package dockerutil constructs Docker Engine API clients shared by both
// the agent and admin binaries.
package dockerutil

import (
	"github.com/docker/docker/client"
)

// New builds a Docker client. If host is empty, the client falls back to the
// standard DOCKER_HOST env var / default unix socket.
func New(host string) (*client.Client, error) {
	opts := []client.Opt{
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	return client.NewClientWithOpts(opts...)
}
