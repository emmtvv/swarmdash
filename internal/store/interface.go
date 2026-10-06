package store

import "time"

// Interface is what the admin package depends on for persistence: users,
// sessions, the audit log, API tokens and registry credentials. Two
// implementations exist: MongoStore (internal/store/mongo.go), where every
// admin replica connects to the same MongoDB deployment so running more
// than one replica for HA needs no separate storage mode, and SQLiteStore
// (internal/store/sqlite.go), which persists the same state in a local
// SQLite database for deployments that don't want to run MongoDB just for
// the admin UI - at the cost of only supporting a single admin replica.
type Interface interface {
	Close() error

	PutUser(u User) error
	GetUser(username string) (User, error)
	GetUserBySSOSubject(subject string) (User, error)
	HasAnyUser() (bool, error)
	ListUsers() ([]User, error)
	DeleteUser(username string) error

	PutSession(sess Session) error
	GetSession(token string) (Session, error)
	DeleteSession(token string) error

	PutLoginAttempt(a LoginAttempt) error
	GetLoginAttempt(key string) (LoginAttempt, error)
	DeleteLoginAttempt(key string) error

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

	PutStackVersion(v StackVersion) error
	// ListStackVersions returns a stack's versions, newest first; an empty
	// stackName lists every stack's.
	ListStackVersions(stackName string) ([]StackVersion, error)
	GetStackVersion(id string) (StackVersion, error)
	// PruneStackVersions deletes all but the newest keep versions of a stack.
	PruneStackVersions(stackName string, keep int) error

	PutStackTemplate(t StackTemplate) error
	ListStackTemplates() ([]StackTemplate, error)
	GetStackTemplate(id string) (StackTemplate, error)
	DeleteStackTemplate(id string) error

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

	GetAppSettings() (AppSettings, error)
	PutAppSettings(a AppSettings) error

	Ping() error
}

var (
	_ Interface = (*MongoStore)(nil)
	_ Interface = (*SQLiteStore)(nil)
)
