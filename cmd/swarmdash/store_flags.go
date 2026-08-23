package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"swarmdash/internal/secretenv"
	"swarmdash/internal/store"
)

// storeFlags holds the --storage-driver/--data-dir/--mongo-* flags shared
// by every subcommand that needs to open admin's configured store (`admin`
// itself, and `rotate-cluster-secret`) - factored out so the flag
// definitions, their env var fallbacks, and the mongo-URI-building logic
// live in exactly one place instead of being copy-pasted per subcommand.
type storeFlags struct {
	storageDriver string
	dataDir       string

	// mongoURI, when set, is used verbatim and every other mongo-* flag/env
	// var below is ignored - it's the escape hatch for connection strings
	// the discrete parts can't express (mongodb+srv, multiple hosts, exotic
	// query params). Otherwise a URI is built up from the discrete parts,
	// which is what deploy/stack.yml uses so the username/password can each
	// come from their own Docker secret.
	mongoURI        string
	mongoHost       string
	mongoPort       string
	mongoUsername   string
	mongoPassword   string
	mongoDB         string
	mongoAuthSource string
	mongoParams     string
}

func (f *storeFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.storageDriver, "storage-driver", "", "where admin state (users, sessions, audit log, tokens, ...) is persisted: \"local\" (default, a SQLite database - see --data-dir) or \"mongo\" (see --mongo-* below; required for more than one admin replica) (env SWARMDASH_STORAGE_DRIVER)")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "", "directory holding the SQLite database when --storage-driver=local (env SWARMDASH_DATA_DIR; defaults to \"./data\"); ignored otherwise. Only one admin replica may point at a given data dir at a time")
	cmd.Flags().StringVar(&f.mongoURI, "mongo-uri", "", "full MongoDB connection string, overrides every other --mongo-* flag below (env SWARMDASH_MONGO_URI, _FILE suffix also works); only used when --storage-driver=mongo. Every admin replica should point at the same MongoDB deployment")
	cmd.Flags().StringVar(&f.mongoHost, "mongo-host", "", "MongoDB host (env SWARMDASH_MONGO_HOST; defaults to \"localhost\"), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&f.mongoPort, "mongo-port", "", "MongoDB port (env SWARMDASH_MONGO_PORT; defaults to \"27017\"), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&f.mongoUsername, "mongo-username", "", "MongoDB username (env SWARMDASH_MONGO_USERNAME, _FILE suffix also works), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&f.mongoPassword, "mongo-password", "", "MongoDB password (env SWARMDASH_MONGO_PASSWORD, _FILE suffix also works), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&f.mongoDB, "mongo-database", "", "MongoDB database name (env SWARMDASH_MONGO_DATABASE; defaults to \"swarmdash\")")
	cmd.Flags().StringVar(&f.mongoAuthSource, "mongo-auth-source", "", "MongoDB authSource (env SWARMDASH_MONGO_AUTH_SOURCE; defaults to \"admin\" when a username is set), ignored if --mongo-uri is set")
	cmd.Flags().StringVar(&f.mongoParams, "mongo-params", "", "extra MongoDB connection string query params, e.g. \"replicaSet=rs0&tls=true\" (env SWARMDASH_MONGO_PARAMS), ignored if --mongo-uri is set")
}

// open resolves the driver/env fallbacks and opens the configured store,
// exactly as `swarmdash admin` does at startup.
func (f *storeFlags) open(ctx context.Context) (store.Interface, error) {
	storageDriver := f.storageDriver
	if storageDriver == "" {
		storageDriver = os.Getenv("SWARMDASH_STORAGE_DRIVER")
	}
	if storageDriver == "" {
		storageDriver = "local"
	}

	switch storageDriver {
	case "mongo":
		mongoURI := f.mongoURI
		if mongoURI == "" {
			resolved, err := secretenv.Resolve("SWARMDASH_MONGO_URI")
			if err != nil {
				return nil, fmt.Errorf("read mongo uri: %w", err)
			}
			mongoURI = resolved
		}
		mongoDB := f.mongoDB
		if mongoDB == "" {
			mongoDB = os.Getenv("SWARMDASH_MONGO_DATABASE")
		}
		if mongoDB == "" {
			mongoDB = "swarmdash"
		}
		if mongoURI == "" {
			mongoHost := f.mongoHost
			if mongoHost == "" {
				mongoHost = os.Getenv("SWARMDASH_MONGO_HOST")
			}
			if mongoHost == "" {
				mongoHost = "localhost"
			}
			mongoPort := f.mongoPort
			if mongoPort == "" {
				mongoPort = os.Getenv("SWARMDASH_MONGO_PORT")
			}
			if mongoPort == "" {
				mongoPort = "27017"
			}
			mongoUsername := f.mongoUsername
			if mongoUsername == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_MONGO_USERNAME")
				if err != nil {
					return nil, fmt.Errorf("read mongo username: %w", err)
				}
				mongoUsername = resolved
			}
			mongoPassword := f.mongoPassword
			if mongoPassword == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_MONGO_PASSWORD")
				if err != nil {
					return nil, fmt.Errorf("read mongo password: %w", err)
				}
				mongoPassword = resolved
			}
			mongoAuthSource := f.mongoAuthSource
			if mongoAuthSource == "" {
				mongoAuthSource = os.Getenv("SWARMDASH_MONGO_AUTH_SOURCE")
			}
			if mongoAuthSource == "" && mongoUsername != "" {
				// MONGO_INITDB_ROOT_USERNAME/PASSWORD (see deploy/stack.yml)
				// always creates the root user in the admin database.
				mongoAuthSource = "admin"
			}
			mongoParams := f.mongoParams
			if mongoParams == "" {
				mongoParams = os.Getenv("SWARMDASH_MONGO_PARAMS")
			}
			mongoURI = buildMongoURI(mongoHost, mongoPort, mongoUsername, mongoPassword, mongoAuthSource, mongoParams)
		}

		return store.OpenMongo(ctx, mongoURI, mongoDB)
	case "local":
		dataDir := f.dataDir
		if dataDir == "" {
			dataDir = os.Getenv("SWARMDASH_DATA_DIR")
		}
		if dataDir == "" {
			dataDir = "./data"
		}
		return store.OpenSQLite(dataDir)
	default:
		return nil, fmt.Errorf("invalid --storage-driver %q: must be \"mongo\" or \"local\"", storageDriver)
	}
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
