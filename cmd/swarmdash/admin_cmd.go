package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"swarmdash/internal/admin"
	"swarmdash/internal/dockerutil"
	"swarmdash/internal/secretenv"
	"swarmdash/internal/store"
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

		// mongoURI, when set, is used verbatim and every other mongo-*
		// flag/env var below is ignored - it's the escape hatch for
		// connection strings the discrete parts can't express (mongodb+srv,
		// multiple hosts, exotic query params). Otherwise a URI is built up
		// from the discrete parts, which is what deploy/stack.yml uses so
		// the username/password can each come from their own Docker secret.
		mongoURI        string
		mongoHost       string
		mongoPort       string
		mongoUsername   string
		mongoPassword   string
		mongoDB         string
		mongoAuthSource string
		mongoParams     string
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

			if mongoURI == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_MONGO_URI")
				if err != nil {
					return fmt.Errorf("read mongo uri: %w", err)
				}
				mongoURI = resolved
			}
			if mongoDB == "" {
				mongoDB = os.Getenv("SWARMDASH_MONGO_DATABASE")
			}
			if mongoDB == "" {
				mongoDB = "swarmdash"
			}
			if mongoURI == "" {
				if mongoHost == "" {
					mongoHost = os.Getenv("SWARMDASH_MONGO_HOST")
				}
				if mongoHost == "" {
					mongoHost = "localhost"
				}
				if mongoPort == "" {
					mongoPort = os.Getenv("SWARMDASH_MONGO_PORT")
				}
				if mongoPort == "" {
					mongoPort = "27017"
				}
				if mongoUsername == "" {
					resolved, err := secretenv.Resolve("SWARMDASH_MONGO_USERNAME")
					if err != nil {
						return fmt.Errorf("read mongo username: %w", err)
					}
					mongoUsername = resolved
				}
				if mongoPassword == "" {
					resolved, err := secretenv.Resolve("SWARMDASH_MONGO_PASSWORD")
					if err != nil {
						return fmt.Errorf("read mongo password: %w", err)
					}
					mongoPassword = resolved
				}
				if mongoAuthSource == "" {
					mongoAuthSource = os.Getenv("SWARMDASH_MONGO_AUTH_SOURCE")
				}
				if mongoAuthSource == "" && mongoUsername != "" {
					// MONGO_INITDB_ROOT_USERNAME/PASSWORD (see deploy/stack.yml)
					// always creates the root user in the admin database.
					mongoAuthSource = "admin"
				}
				if mongoParams == "" {
					mongoParams = os.Getenv("SWARMDASH_MONGO_PARAMS")
				}
				mongoURI = buildMongoURI(mongoHost, mongoPort, mongoUsername, mongoPassword, mongoAuthSource, mongoParams)
			}

			mdb, err := store.OpenMongo(cmd.Context(), mongoURI, mongoDB)
			if err != nil {
				return fmt.Errorf("open mongo store: %w", err)
			}
			var st store.Interface = mdb
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
	cmd.Flags().StringVar(&mongoURI, "mongo-uri", "", "full MongoDB connection string, overrides every other --mongo-* flag below (env SWARMDASH_MONGO_URI, _FILE suffix also works). Every admin replica should point at the same MongoDB deployment")
	cmd.Flags().StringVar(&mongoHost, "mongo-host", "", "MongoDB host (env SWARMDASH_MONGO_HOST; defaults to \"localhost\"), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&mongoPort, "mongo-port", "", "MongoDB port (env SWARMDASH_MONGO_PORT; defaults to \"27017\"), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&mongoUsername, "mongo-username", "", "MongoDB username (env SWARMDASH_MONGO_USERNAME, _FILE suffix also works), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&mongoPassword, "mongo-password", "", "MongoDB password (env SWARMDASH_MONGO_PASSWORD, _FILE suffix also works), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&mongoDB, "mongo-database", "", "MongoDB database name (env SWARMDASH_MONGO_DATABASE; defaults to \"swarmdash\")")
	cmd.Flags().StringVar(&mongoAuthSource, "mongo-auth-source", "", "MongoDB authSource (env SWARMDASH_MONGO_AUTH_SOURCE; defaults to \"admin\" when a username is set), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&mongoParams, "mongo-params", "", "extra MongoDB connection string query params, e.g. \"replicaSet=rs0&tls=true\" (env SWARMDASH_MONGO_PARAMS), ignored if --mongo-uri is set")

	return cmd
}

// buildMongoURI assembles a mongodb:// connection string from discrete
// parts. It's the deploy/stack.yml-friendly counterpart to --mongo-uri:
// username and password can each come from their own Docker secret instead
// of being baked together into one connection-string secret.
func buildMongoURI(host, port, username, password, authSource, params string) string {
	// Mongo's URI parser requires a "/" between the host and a query
	// string even with no database path segment (mongodb://host:port/?...),
	// unlike net/url's default rendering when Path is left empty.
	u := url.URL{Scheme: "mongodb", Host: net.JoinHostPort(host, port), Path: "/"}
	if username != "" {
		if password != "" {
			u.User = url.UserPassword(username, password)
		} else {
			u.User = url.User(username)
		}
	}
	q, _ := url.ParseQuery(params)
	if q == nil {
		q = url.Values{}
	}
	if authSource != "" {
		q.Set("authSource", authSource)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
