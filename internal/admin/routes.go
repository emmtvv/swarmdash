package admin

import "net/http"

// route pairs a pattern registered with http.ServeMux (e.g. "GET
// /nodes/{id}") with its handler and an explicit RBAC decision. ViewerOK
// marks a route reachable by a "viewer" account; everything else -
// including any route added here without setting it - requires "admin".
// This is the single source of truth: Handler() registers the real mux
// from it, and rbac (below) builds the lookup requireRole uses, so a route
// can't be dispatched without someone having decided its access level.
type route struct {
	Pattern  string
	Handler  http.HandlerFunc
	ViewerOK bool
}

// protectedRoutes lists every route behind requireAuth+requireRole. Kept as
// one literal table (rather than a sequence of mux.HandleFunc calls) so the
// RBAC test can iterate it directly instead of guessing at deny-listed
// paths - see TestRequireRole_AllRoutesHaveExplicitRBACDecision.
func protectedRoutes(s *Server) []route {
	return []route{
		{"GET /{$}", s.handleDashboard, true},

		{"GET /account/password", s.handleAccountPasswordPage, true},
		{"POST /account/password", s.handleAccountPasswordChange, true},

		{"GET /nodes", s.handleNodesPage, true},
		{"GET /nodes/stats", s.handleNodesStatsJSON, true},
		{"GET /nodes/{id}", s.handleNodeDetail, true},
		{"GET /nodes/{id}/stats", s.handleNodeStatsJSON, true},
		{"POST /nodes/{id}/availability", s.handleNodeAvailability, false},
		{"POST /nodes/{id}/promote", s.handleNodePromote, false},
		{"POST /nodes/{id}/demote", s.handleNodeDemote, false},
		{"POST /nodes/{id}/labels", s.handleNodeLabelAdd, false},
		{"POST /nodes/{id}/labels/{key}/delete", s.handleNodeLabelDelete, false},

		{"GET /stacks", s.handleStacksPage, true},
		{"GET /stacks/templates", s.handleTemplatesPage, true},
		{"POST /stacks/templates", s.handleTemplateSave, false},
		{"POST /stacks/templates/{id}/delete", s.handleTemplateDelete, false},
		{"GET /stacks/deploy", s.handleStackDeployPage, true},
		{"POST /stacks/deploy/preview", s.handleStackDeployPreview, false},
		{"POST /stacks/deploy", s.handleStackDeploySubmit, false},
		{"GET /stacks/gitops", s.handleGitOpsPage, true},
		{"POST /stacks/gitops", s.handleGitOpsCreate, false},
		{"POST /stacks/gitops/{id}/sync", s.handleGitOpsSync, false},
		{"POST /stacks/gitops/{id}/delete", s.handleGitOpsDelete, false},
		{"GET /stacks/{name}", s.handleStackDetail, true},
		{"GET /stacks/{name}/export.yml", s.handleStackExport, true},
		{"POST /stacks/{name}/restart", s.handleStackRestart, false},
		{"POST /stacks/{name}/delete", s.handleStackDelete, false},
		{"GET /stacks/{name}/versions/{id}/compose.yml", s.handleStackVersionDownload, true},
		{"POST /stacks/{name}/versions/{id}/rollback", s.handleStackRollback, false},

		{"GET /services", s.handleServicesPage, true},
		{"GET /services/new", s.handleServiceNewPage, true},
		{"POST /services", s.handleServiceCreate, false},
		{"GET /services/{name}", s.handleServiceDetail, true},
		{"GET /services/{name}/events", s.handleServiceEvents, true},
		{"GET /services/{name}/export.yml", s.handleServiceExport, true},
		{"POST /services/{name}/scale", s.handleServiceScale, false},
		{"POST /services/{name}/image", s.handleServiceUpdateImage, false},
		{"POST /services/{name}/spec", s.handleServiceUpdateSpec, false},
		{"POST /services/{name}/rollback", s.handleServiceRollback, false},
		{"POST /services/{name}/restart", s.handleServiceRestart, false},
		{"POST /services/{name}/update-latest", s.handleServiceUpdateLatest, false},
		{"POST /services/{name}/delete", s.handleServiceDelete, false},
		{"POST /services/bulk", s.handleServicesBulk, false},
		{"POST /services/{name}/deploy-hooks", s.handleDeployHookCreate, false},
		{"POST /services/{name}/deploy-hooks/{id}/delete", s.handleDeployHookDelete, false},

		{"GET /topology", s.handleTopologyPage, true},

		{"GET /networks", s.handleNetworksPage, true},
		{"POST /networks", s.handleNetworkCreate, false},
		{"POST /networks/{id}/delete", s.handleNetworkDelete, false},

		{"GET /secrets", s.handleSecretsPage, true},
		{"POST /secrets", s.handleSecretCreate, false},
		{"GET /secrets/{id}", s.handleSecretDetail, true},
		{"POST /secrets/{id}/rotate", s.handleSecretRotate, false},
		{"POST /secrets/{id}/delete", s.handleSecretDelete, false},

		{"GET /configs", s.handleConfigsPage, true},
		{"POST /configs", s.handleConfigCreate, false},
		// Unlike a secret's, a config's content can be read back - and
		// configs routinely carry credentials anyway (an nginx htpasswd,
		// an app config with a DSN), so the detail page is admin-only.
		{"GET /configs/{id}", s.handleConfigDetail, false},
		{"POST /configs/{id}/rotate", s.handleConfigRotate, false},
		{"POST /configs/{id}/delete", s.handleConfigDelete, false},

		{"GET /images", s.handleImagesPage, true},
		{"POST /images/prune", s.handleImagesPrune, false},

		{"GET /volumes", s.handleVolumesPage, true},
		{"POST /volumes", s.handleVolumeCreate, false},
		{"POST /volumes/{name}/delete", s.handleVolumeDelete, false},

		{"GET /audit", s.handleAuditPage, true},
		{"GET /events", s.handleEventsPage, true},

		// Settings are entirely admin-only: registry/webhook/token/SSO
		// credentials, join tokens, and user management all live here.
		{"GET /settings/webhooks", s.handleWebhooksPage, false},
		{"POST /settings/webhooks", s.handleWebhookCreate, false},
		{"POST /settings/webhooks/{id}/delete", s.handleWebhookDelete, false},

		{"GET /settings/tokens", s.handleTokensPage, false},
		{"POST /settings/tokens", s.handleTokenCreate, false},
		{"POST /settings/tokens/{id}/delete", s.handleTokenDelete, false},

		{"GET /settings/users", s.handleUsersPage, false},
		{"POST /settings/users", s.handleUserCreate, false},
		{"POST /settings/users/{username}/delete", s.handleUserDelete, false},
		{"POST /settings/users/{username}/password", s.handleUserResetPassword, false},

		{"GET /settings/swarm", s.handleSwarmPage, false},
		{"POST /settings/swarm/spec", s.handleSwarmUpdateSpec, false},
		{"POST /settings/swarm/rotate-token/{role}", s.handleSwarmRotateToken, false},
		{"POST /settings/swarm/rotate-ca", s.handleSwarmForceCertRotate, false},
		{"POST /settings/swarm/rebalance", s.handleSwarmRebalance, false},
		{"POST /settings/swarm/prune-failed-tasks", s.handleSwarmPruneFailedTasks, false},
		{"POST /settings/swarm/prune-resources", s.handleSwarmPruneResources, false},

		{"GET /settings/registries", s.handleRegistriesPage, false},
		{"POST /settings/registries", s.handleRegistryCreate, false},
		{"POST /settings/registries/{server}/delete", s.handleRegistryDelete, false},

		{"GET /settings/sso", s.handleSSOSettingsPage, false},
		{"POST /settings/sso", s.handleSSOSettingsSave, false},
		{"POST /settings/sso/disable", s.handleSSOSettingsDisable, false},

		{"GET /settings/backup", s.handleBackupPage, false},
		{"GET /settings/backup/export", s.handleBackupExport, false},
		{"POST /settings/backup/restore", s.handleBackupRestore, false},

		{"GET /settings/general", s.handleGeneralSettingsPage, false},
		{"POST /settings/general", s.handleGeneralSettingsSave, false},

		// The interactive console (page + websocket) lets you run arbitrary
		// commands in a container despite being a GET - admin-only. Same
		// for the file browser/download: containers routinely bind-mount
		// Docker secrets (e.g. /run/secrets/*), so letting a viewer read
		// arbitrary paths out of them is a straight secrets exfiltration
		// path.
		{"GET /exec/{taskID}", s.handleExecPage, false},
		{"GET /ws/exec/{taskID}", s.handleExecProxy, false},
		{"GET /files/{taskID}", s.handleFilesList, false},
		{"GET /files/{taskID}/download", s.handleFileDownload, false},

		{"GET /logs/{taskID}", s.handleTaskLogsPage, true},
		{"GET /ws/logs/{taskID}", s.handleLogsProxy, true},
		{"GET /ws/stats/{taskID}", s.handleStatsProxy, true},
		{"GET /logs/{taskID}/download", s.handleLogsDownload, true},

		{"GET /services/{name}/logs", s.handleServiceLogsPage, true},
		{"GET /ws/services/{name}/logs", s.handleServiceLogsProxy, true},
		{"GET /services/{name}/logs/download", s.handleServiceLogsDownload, true},
	}
}

// rbac resolves which registered route a request matches - via a ServeMux
// built from the same pattern set Handler() registers, so route matching
// (which param wins, trailing-slash handling, etc.) is exactly the
// stdlib's own, not a second hand-rolled implementation - and reports
// whether that route requires the admin role. A request matching no
// pattern here fails closed to admin-only; in practice that's dead code,
// since requireRole only ever sees requests already dispatched by the
// identically-built `protected` mux in Handler().
type rbac struct {
	mux      *http.ServeMux
	adminFor map[string]bool
}

func newRBAC(routes []route) *rbac {
	mux := http.NewServeMux()
	adminFor := make(map[string]bool, len(routes))
	noop := func(http.ResponseWriter, *http.Request) {}
	for _, rt := range routes {
		mux.HandleFunc(rt.Pattern, noop)
		adminFor[rt.Pattern] = !rt.ViewerOK
	}
	return &rbac{mux: mux, adminFor: adminFor}
}

// needsAdmin reports whether r requires the admin role.
func (rb *rbac) needsAdmin(r *http.Request) bool {
	// Changing your own password isn't a privileged action - every account
	// needs it, including a viewer stuck behind MustChangePassword.
	if r.URL.Path == "/account/password" {
		return false
	}
	_, pattern := rb.mux.Handler(r)
	admin, ok := rb.adminFor[pattern]
	if !ok {
		return true
	}
	return admin
}
