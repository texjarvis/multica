package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestTaskActorFromRequest(t *testing.T) {
	userID := uuid.NewString()
	agentID := uuid.NewString()
	taskID := uuid.NewString()
	workspaceID := uuid.NewString()

	complete := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	complete.Header.Set("X-Actor-Source", "task_token")
	complete.Header.Set("X-User-ID", userID)
	complete.Header.Set("X-Agent-ID", agentID)
	complete.Header.Set("X-Task-ID", taskID)
	complete.Header.Set("X-Workspace-ID", workspaceID)
	actor, ok := TaskActorFromRequest(complete)
	if !ok {
		t.Fatal("complete task token was rejected")
	}
	if actor.UserID != userID || actor.AgentID != agentID || actor.TaskID != taskID || actor.WorkspaceID != workspaceID {
		t.Fatalf("actor = %+v", actor)
	}

	missingSource := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	missingSource.Header.Set("X-User-ID", userID)
	missingSource.Header.Set("X-Agent-ID", agentID)
	missingSource.Header.Set("X-Task-ID", taskID)
	missingSource.Header.Set("X-Workspace-ID", workspaceID)
	if _, ok := TaskActorFromRequest(missingSource); ok {
		t.Fatal("missing actor source was accepted")
	}

	incomplete := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	incomplete.Header.Set("X-Actor-Source", "task_token")
	incomplete.Header.Set("X-User-ID", userID)
	incomplete.Header.Set("X-Workspace-ID", workspaceID)
	if _, ok := TaskActorFromRequest(incomplete); ok {
		t.Fatal("incomplete task token was accepted")
	}

	cloud := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	cloud.Header.Set("X-Actor-Source", "cloud_pat")
	cloud.Header.Set("X-User-ID", userID)
	cloud.Header.Set("X-Agent-ID", agentID)
	cloud.Header.Set("X-Task-ID", taskID)
	cloud.Header.Set("X-Workspace-ID", workspaceID)
	if _, ok := TaskActorFromRequest(cloud); ok {
		t.Fatal("cloud pat was accepted as a task actor")
	}

	badID := complete.Clone(complete.Context())
	badID.Header.Set("X-Task-ID", "not-a-uuid")
	if _, ok := TaskActorFromRequest(badID); ok {
		t.Fatal("non-uuid task id was accepted")
	}
}
