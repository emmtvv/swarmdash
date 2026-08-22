package store

import "time"

// Interface is what the admin package depends on for persistence: users,
// sessions, the audit log, API tokens and registry credentials. The only
// implementation is MongoStore (internal/store/mongo.go): every admin
// replica connects to the same MongoDB deployment, so running more than
// one replica for HA needs no separate storage mode.
type Interface interface {
	Close() error

	PutUser(u User) error
	GetUser(username string) (User, error)
	HasAnyUser() (bool, error)
	ListUsers() ([]User, error)
	DeleteUser(username string) error

	PutSession(sess Session) error
	GetSession(token string) (Session, error)
	DeleteSession(token string) error

	PutLoginAttempt(a LoginAttempt) error
	GetLoginAttempt(username string) (LoginAttempt, error)
	DeleteLoginAttempt(username string) error

	AppendAudit(e AuditEntry) error
	ListAudit(skip, limit int) ([]AuditEntry, error)
	CountAudit() (int, error)

	PutAPIToken(t APIToken) error
	ListAPITokens() ([]APIToken, error)
	FindAPITokenByHash(hash string) (APIToken, error)
	TouchAPIToken(id string) error
	DeleteAPIToken(id string) error

	PutRegistryCredential(c RegistryCredential) error
	GetRegistryCredential(server string) (RegistryCredential, error)
	ListRegistryCredentials() ([]RegistryCredential, error)
	DeleteRegistryCredential(server string) error

	AppendTaskEvent(e TaskEvent) error
	ListTaskEvents(skip, limit int, serviceName string) ([]TaskEvent, error)
	CountTaskEvents(serviceName string) (int, error)

	PutWebhook(w Webhook) error
	ListWebhooks() ([]Webhook, error)
	DeleteWebhook(id string) error

	PutGitStack(g GitStack) error
	ListGitStacks() ([]GitStack, error)
	GetGitStack(id string) (GitStack, error)
	DeleteGitStack(id string) error

	PutDeployHook(h DeployHook) error
	ListDeployHooks() ([]DeployHook, error)
	ListDeployHooksForService(serviceName string) ([]DeployHook, error)
	FindDeployHookByHash(hash string) (DeployHook, error)
	TouchDeployHook(id string) error
	DeleteDeployHook(id string) error

	AppendClusterSample(cs ClusterSample) error
	ListClusterSamples(since time.Time) ([]ClusterSample, error)
	PruneClusterSamples(olderThan time.Time) error

	GetSSOConfig() (SSOConfig, error)
	PutSSOConfig(c SSOConfig) error

	Ping() error
}

var _ Interface = (*MongoStore)(nil)
