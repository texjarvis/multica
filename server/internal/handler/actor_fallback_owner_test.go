package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestResolveActorLegacyFallbackRequiresAuthenticatedAgentOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	foreignOwnerID := createDaemonAuthMember(t, "member")
	agentName := "fallback-owner-" + uuid.NewString()
	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args
		)
		VALUES ($1, $2, '', 'local', '{}'::jsonb, $3, 'private', 1, $4, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, agentName, handlerTestRuntimeID(t), foreignOwnerID).Scan(&agentID); err != nil {
		t.Fatalf("create foreign agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})
	taskID := createHandlerTestTaskForAgent(t, agentID)

	req := newRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)

	actorType, actorID := testHandler.resolveActor(req, testUserID, testWorkspaceID)
	if actorType != "member" || actorID != testUserID {
		t.Fatalf("cross-owner fallback = %s/%s, want member/%s", actorType, actorID, testUserID)
	}

	actorType, actorID = testHandler.resolveActor(req, foreignOwnerID, testWorkspaceID)
	if actorType != "agent" || actorID != agentID {
		t.Fatalf("same-owner fallback = %s/%s, want agent/%s", actorType, actorID, agentID)
	}
}
