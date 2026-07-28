package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSensitiveProtectedRoutesRejectEveryMachineActor(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/me"},
		{http.MethodPatch, "/api/me"},
		{http.MethodPost, "/api/me/onboarding/complete"},
		{http.MethodPost, "/api/cli-token"},
		{http.MethodGet, "/api/tokens"},
		{http.MethodPost, "/api/tokens"},
		{http.MethodPost, "/api/tokens/current/renew"},
		{http.MethodDelete, "/api/tokens/token-1"},
		{http.MethodGet, "/api/workspaces"},
		{http.MethodPost, "/api/workspaces"},
		{http.MethodPatch, "/api/workspaces/workspace-1"},
		{http.MethodPost, "/api/workspaces/workspace-1/members"},
		{http.MethodPatch, "/api/workspaces/workspace-1/members/member-1"},
		{http.MethodDelete, "/api/workspaces/workspace-1/members/member-1"},
		{http.MethodPost, "/api/workspaces/workspace-1/runtime-profiles"},
		{http.MethodPatch, "/api/workspaces/workspace-1/runtime-profiles/profile-1"},
		{http.MethodGet, "/api/workspaces/workspace-1/github/installations"},
		{http.MethodPost, "/api/workspaces/workspace-1/vcs/connections"},
		{http.MethodPost, "/api/workspaces/workspace-1/lark/install/begin"},
		{http.MethodPost, "/api/workspaces/workspace-1/slack/install/byo"},
		{http.MethodGet, "/api/invitations"},
		{http.MethodPost, "/api/invitations/invite-1/accept"},
		{http.MethodPost, "/api/lark/binding/redeem"},
		{http.MethodPost, "/api/slack/binding/redeem"},
		{http.MethodGet, "/api/integrations/composio/connections"},
		{http.MethodPost, "/api/integrations/composio/connect/init"},
		{http.MethodDelete, "/api/integrations/composio/connections/connection-1"},
		{http.MethodGet, "/api/agents/agent-1/env"},
		{http.MethodPut, "/api/agents/agent-1/env"},
		{http.MethodGet, "/api/agents/agent-1/composio-toolkit-allowlist"},
		{http.MethodPut, "/api/agents/agent-1/composio-toolkit-allowlist"},
		{http.MethodPost, "/api/agent-builder/sessions"},
		{http.MethodPost, "/api/cloud-runtime/nodes/exec"},
		{http.MethodDelete, "/api/cloud-runtime/nodes"},
		{http.MethodPatch, "/api/runtimes/runtime-1"},
		{http.MethodDelete, "/api/runtimes/runtime-1"},
		{http.MethodPut, "/api/notification-preferences"},
		{http.MethodGet, "/api/inbox"},
		{http.MethodGet, "/api/dashboard/usage/daily"},
		{http.MethodGet, "/api/pins"},
		{http.MethodPost, "/api/pins"},
		{http.MethodGet, "/api/chat/pinned-agents"},
		{http.MethodPut, "/api/chat/pinned-agents"},
		{http.MethodGet, "/api/assignee-frequency"},
		{http.MethodPost, "/api/feedback"},
		{http.MethodPost, "/api/agents"},
		{http.MethodPost, "/api/agents/from-template"},
		{http.MethodPost, "/api/agents/agent-1/archive"},
		{http.MethodPost, "/api/agents/agent-1/restore"},
		{http.MethodPut, "/api/agents/agent-1/skills"},
		{http.MethodPost, "/api/agents/agent-1/skills/add"},
		{http.MethodPut, "/api/agents/agent-1/skills/skill-1/enabled"},
		{http.MethodDelete, "/api/agents/agent-1/skills/skill-1"},
		{http.MethodPut, "/api/agents/agent-1/runtime-skills/enabled"},
		{http.MethodPost, "/api/skills"},
		{http.MethodPost, "/api/skills/import"},
		{http.MethodPut, "/api/skills/skill-1"},
		{http.MethodDelete, "/api/skills/skill-1"},
		{http.MethodPut, "/api/skills/skill-1/files/SKILL.md"},
		{http.MethodPost, "/api/skills/skill-1/labels"},
	}
	for _, actorSource := range []string{"task_token", "cloud_pat"} {
		for _, route := range routes {
			t.Run(actorSource+"_"+route.method+"_"+route.path, func(t *testing.T) {
				reached := false
				protected := requireHumanOnSensitiveRoutes(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					reached = true
				}))
				req := httptest.NewRequest(route.method, route.path, nil)
				req.Header.Set("X-Actor-Source", actorSource)
				w := httptest.NewRecorder()
				protected.ServeHTTP(w, req)
				if w.Code != http.StatusForbidden || reached {
					t.Fatalf("status=%d reached=%v, want 403/false", w.Code, reached)
				}
			})
		}
	}
}

func TestSensitiveRouteGuardPreservesMachineAutomationRoutes(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/issues"},
		{http.MethodPost, "/api/comments/comment-1"},
		{http.MethodPost, "/api/chat/sessions"},
		{http.MethodPost, "/api/tasks/task-1/cancel"},
		{http.MethodPut, "/api/agents/agent-1"},
		{http.MethodGet, "/api/agents/agent-1/skills"},
		{http.MethodGet, "/api/skills"},
		{http.MethodGet, "/api/skills/skill-1"},
		{http.MethodGet, "/api/skills/skill-1/files"},
		{http.MethodGet, "/api/workspaces/workspace-1/members"},
	}
	for _, route := range routes {
		t.Run(route.method+"_"+route.path, func(t *testing.T) {
			for _, actorSource := range []string{"task_token", "cloud_pat"} {
				t.Run(actorSource, func(t *testing.T) {
					protected := requireHumanOnSensitiveRoutes(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(http.StatusNoContent)
					}))
					req := httptest.NewRequest(route.method, route.path, nil)
					req.Header.Set("X-Actor-Source", actorSource)
					w := httptest.NewRecorder()
					protected.ServeHTTP(w, req)
					if w.Code != http.StatusNoContent {
						t.Fatalf("status=%d, want 204", w.Code)
					}
				})
			}
		})
	}
}
