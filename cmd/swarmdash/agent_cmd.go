package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"swarmdash/internal/agent"
	"swarmdash/internal/dockerutil"
	"swarmdash/internal/secretenv"
)

func newAgentCmd() *cobra.Command {
	var (
		listenAddr string
		dockerHost string
		secret     string
		tlsCert    string
		tlsKey     string
		tlsCA      string
	)

	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the per-node agent that exposes exec/logs/stats to admin",
		RunE: func(cmd *cobra.Command, args []string) error {
			if secret == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_CLUSTER_SECRET")
				if err != nil {
					return fmt.Errorf("read cluster secret: %w", err)
				}
				secret = resolved
			}
			if secret == "" {
				return fmt.Errorf("cluster secret is required: pass --cluster-secret or set SWARMDASH_CLUSTER_SECRET (must match the value given to swarmdash admin)")
			}

			docker, err := dockerutil.New(dockerHost)
			if err != nil {
				return fmt.Errorf("connect to docker: %w", err)
			}
			defer docker.Close()

			if _, err := docker.Ping(cmd.Context()); err != nil {
				return fmt.Errorf("ping docker daemon: %w", err)
			}

			srv := agent.New(agent.Config{
				ListenAddr:    listenAddr,
				DockerHost:    dockerHost,
				ClusterSecret: secret,
				TLSCertFile:   tlsCert,
				TLSKeyFile:    tlsKey,
				TLSCAFile:     tlsCA,
			}, docker)

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			return srv.ListenAndServe(ctx)
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":8871", "address to listen on")
	cmd.Flags().StringVar(&dockerHost, "docker-host", "", "docker daemon socket/URL (defaults to DOCKER_HOST or unix:///var/run/docker.sock)")
	cmd.Flags().StringVar(&secret, "cluster-secret", "", "shared secret authenticating admin -> agent requests (env SWARMDASH_CLUSTER_SECRET)")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "agent server certificate (from `swarmdash tls init`); enables mTLS when set with --tls-key and --tls-ca")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "agent server private key")
	cmd.Flags().StringVar(&tlsCA, "tls-ca", "", "CA certificate used to verify admin's client certificate")

	return cmd
}
