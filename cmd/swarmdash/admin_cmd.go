package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"swarmdash/internal/admin"
	"swarmdash/internal/dockerutil"
	"swarmdash/internal/secretenv"
)

func newAdminCmd() *cobra.Command {
	var (
		listenAddr string
		dockerHost string
		secret     string
		agentPort  string
		bootUser   string
		bootPass   string

		agentTLSCert string
		agentTLSKey  string
		agentTLSCA   string
		uiTLSCert    string
		uiTLSKey     string

		trustedProxies []string

		sf storeFlags
	)

	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Run the admin web UI/API on a manager node",
		RunE: func(cmd *cobra.Command, args []string) error {
			if secret == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_CLUSTER_SECRET")
				if err != nil {
					return fmt.Errorf("read cluster secret: %w", err)
				}
				secret = resolved
			}
			if secret == "" {
				return fmt.Errorf("cluster secret is required: pass --cluster-secret or set SWARMDASH_CLUSTER_SECRET (must match the value given to swarmdash agent on every node)")
			}
			if bootUser == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_ADMIN_USERNAME")
				if err != nil {
					return fmt.Errorf("read bootstrap username: %w", err)
				}
				bootUser = resolved
			}
			if bootUser == "" {
				bootUser = "admin"
			}
			if bootPass == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_ADMIN_PASSWORD")
				if err != nil {
					return fmt.Errorf("read bootstrap password: %w", err)
				}
				bootPass = resolved
			}

			docker, err := dockerutil.New(dockerHost)
			if err != nil {
				return fmt.Errorf("connect to docker: %w", err)
			}
			defer docker.Close()

			info, err := docker.Info(cmd.Context())
			if err != nil {
				return fmt.Errorf("ping docker daemon: %w", err)
			}
			if !info.Swarm.ControlAvailable {
				return fmt.Errorf("this node is not a swarm manager (or swarm mode is not active): admin must run on a manager node")
			}

			st, err := sf.open(cmd.Context())
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer st.Close()

			srv, err := admin.New(admin.Config{
				ListenAddr:        listenAddr,
				DockerHost:        dockerHost,
				ClusterSecret:     secret,
				AgentPort:         agentPort,
				BootstrapUsername: bootUser,
				BootstrapPassword: bootPass,
				AgentTLSCertFile:  agentTLSCert,
				AgentTLSKeyFile:   agentTLSKey,
				AgentTLSCAFile:    agentTLSCA,
				UITLSCertFile:     uiTLSCert,
				UITLSKeyFile:      uiTLSKey,
				TrustedProxies:    trustedProxies,
			}, docker, st)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			return srv.ListenAndServe(ctx)
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":8870", "address for the web UI/API to listen on")
	cmd.Flags().StringVar(&dockerHost, "docker-host", "", "docker daemon socket/URL (defaults to DOCKER_HOST or unix:///var/run/docker.sock)")
	cmd.Flags().StringVar(&secret, "cluster-secret", "", "shared secret authenticating admin -> agent requests (env SWARMDASH_CLUSTER_SECRET)")
	cmd.Flags().StringVar(&agentPort, "agent-port", "8871", "port the swarmdash agent listens on across the cluster")
	cmd.Flags().StringVar(&bootUser, "bootstrap-username", "", "username to create on first run if no users exist yet (env SWARMDASH_ADMIN_USERNAME; defaults to \"admin\")")
	cmd.Flags().StringVar(&bootPass, "bootstrap-password", "", "password to create on first run (env SWARMDASH_ADMIN_PASSWORD; random if unset)")
	cmd.Flags().StringVar(&agentTLSCert, "agent-tls-cert", "", "admin client certificate for dialing agents (from `swarmdash tls init`); enables mTLS when set with --agent-tls-key and --agent-tls-ca")
	cmd.Flags().StringVar(&agentTLSKey, "agent-tls-key", "", "admin client private key")
	cmd.Flags().StringVar(&agentTLSCA, "agent-tls-ca", "", "CA certificate used to verify agents' server certificate")
	cmd.Flags().StringVar(&uiTLSCert, "ui-tls-cert", "", "TLS certificate for the admin web UI/API (enables HTTPS when set with --ui-tls-key)")
	cmd.Flags().StringVar(&uiTLSKey, "ui-tls-key", "", "TLS private key for the admin web UI/API")
	cmd.Flags().StringSliceVar(&trustedProxies, "trusted-proxies", nil, "IPs/CIDRs of reverse proxies (e.g. Traefik/nginx) allowed to set X-Forwarded-For and X-Forwarded-Proto; comma-separated, repeatable. Unset (default) means neither header is trusted: every proxied request is seen as coming from the proxy itself, which collapses per-IP rate limiting and login lockout onto one shared bucket - set this when admin sits behind a reverse proxy")
	sf.register(cmd)

	return cmd
}
