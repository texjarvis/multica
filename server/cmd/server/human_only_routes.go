package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	provisioningBypassFallthrough = iota
	provisioningBypassAllow
	provisioningBypassResponded
)

// requireHumanOnSensitiveRoutes centralizes the protected-API scopes where a
// machine credential must not inherit its owner's authority. It is installed
// immediately after Auth, which has stripped client actor headers and stamped
// authoritative task_token/cloud_pat sources.
func requireHumanOnSensitiveRoutes(next http.Handler) http.Handler {
	return requireHumanOnSensitiveRoutesWithQueries(nil)(next)
}

func requireHumanOnSensitiveRoutesWithQueries(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		humanOnly := handler.RequireHumanActor(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isHumanOnlyProtectedRoute(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			switch provisioningRouteBypass(w, r, queries) {
			case provisioningBypassAllow:
				next.ServeHTTP(w, r)
			case provisioningBypassResponded:
				return
			default:
				humanOnly.ServeHTTP(w, r)
			}
		})
	}
}

func provisioningRouteBypass(w http.ResponseWriter, r *http.Request, queries *db.Queries) int {
	if queries == nil || !provisioningRouteEligible(r.Method, r.URL.Path) {
		return provisioningBypassFallthrough
	}
	if r.Header.Get("X-Actor-Source") != "task_token" {
		return provisioningBypassFallthrough
	}
	actor, ok := middleware.TaskActorFromRequest(r)
	if !ok {
		return provisioningBypassFallthrough
	}
	workspaceID, err := util.ParseUUID(actor.WorkspaceID)
	if err != nil {
		return provisioningBypassFallthrough
	}
	agentID, err := util.ParseUUID(actor.AgentID)
	if err != nil {
		return provisioningBypassFallthrough
	}
	// Includes an expired active row so the handler can deny it as expired
	// and write the audit. Revoked rows do not match, and this probe is not
	// itself the authorization decision.
	exists, err := queries.ActiveProvisioningGrantExists(r.Context(), db.ActiveProvisioningGrantExistsParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "provisioning authorization unavailable"})
		return provisioningBypassResponded
	}
	if !exists {
		return provisioningBypassFallthrough
	}
	return provisioningBypassAllow
}

func provisioningRouteEligible(method, path string) bool {
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if method == http.MethodPost && len(parts) == 2 && parts[0] == "api" && parts[1] == "agents" {
		return true
	}
	if len(parts) >= 4 && parts[0] == "api" && parts[1] == "agents" && parts[2] != "" {
		if method == http.MethodPut && len(parts) == 4 && parts[3] == "skills" {
			return true
		}
		if method == http.MethodPost && len(parts) == 5 && parts[3] == "skills" && parts[4] == "add" {
			return true
		}
	}
	return method == http.MethodGet && len(parts) == 2 && parts[0] == "api" && parts[1] == "runtimes"
}

func isHumanOnlyProtectedRoute(method, path string) bool {
	path = strings.TrimSuffix(path, "/")

	// Task automation resolves human assignees by ID/name through this one
	// read-only endpoint. Keep that exact lookup machine-capable; the handler
	// returns a value-minimized projection to machine credentials. Every
	// workspace mutation and every other workspace/admin/integration route
	// remains human-only below.
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if method == http.MethodGet &&
		len(parts) == 4 &&
		parts[0] == "api" &&
		parts[1] == "workspaces" &&
		parts[2] != "" &&
		parts[3] == "members" {
		return false
	}

	if isHumanOnlyAgentMutation(method, parts) {
		return true
	}
	if method != http.MethodGet &&
		len(parts) >= 2 &&
		parts[0] == "api" &&
		parts[1] == "skills" {
		// Skill definitions and their files/labels become executable context
		// for agents. Machine actors may inspect them but may not mutate them.
		return true
	}

	for _, prefix := range []string{
		"/api/me",
		"/api/cli-token",
		"/api/tokens",
		"/api/workspaces",
		"/api/invitations",
		"/api/lark/binding",
		"/api/slack/binding",
		"/api/integrations/composio",
		"/api/agent-builder",
		"/api/cloud-runtime",
		"/api/runtimes",
		"/api/agent-provisioning-grants",
		"/api/notification-preferences",
		"/api/inbox",
		"/api/dashboard",
		"/api/pins",
		"/api/chat/pinned-agents",
		"/api/assignee-frequency",
		"/api/feedback",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}

	// Agent metadata remains machine-capable, but dedicated plaintext /
	// integration-footprint endpoints are human-only.
	return len(parts) == 4 &&
		parts[0] == "api" &&
		parts[1] == "agents" &&
		parts[2] != "" &&
		(parts[3] == "env" || parts[3] == "composio-toolkit-allowlist")
}

func isHumanOnlyAgentMutation(method string, parts []string) bool {
	if len(parts) < 2 || parts[0] != "api" || parts[1] != "agents" {
		return false
	}

	// Persistent agent creation is human-only. Machine actors may delegate
	// work to existing agents through issues/comments, but may not create a
	// new identity that inherits a runtime profile's fixed configuration.
	if method == http.MethodPost &&
		(len(parts) == 2 || (len(parts) == 3 && parts[2] == "from-template")) {
		return true
	}

	if len(parts) < 4 || parts[2] == "" {
		return false
	}
	if method == http.MethodPost && (parts[3] == "archive" || parts[3] == "restore") {
		return true
	}

	// Reads remain available for issue/comment rendering. Every agent skill or
	// runtime-skill mutation changes executable context and is human-only.
	return method != http.MethodGet &&
		(parts[3] == "skills" || parts[3] == "runtime-skills")
}
