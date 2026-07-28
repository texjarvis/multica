package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestRejectMachineAgentUpdateExactAllowlist(t *testing.T) {
	deniedFields := []string{
		"instructions",
		"runtime_id", "runtime_mode", "runtime_profile_id", "profile_id",
		"runtime_config", "runtime_config_intent",
		"custom_env", "custom_args", "custom_args_intent",
		"mcp_config", "mcp_config_intent",
		"visibility", "permission_mode", "invocation_targets",
		"owner_id", "access",
		"skill_ids", "skills", "disabled_runtime_skills",
		"max_concurrent_tasks", "status",
		"model", "thinking_level", "service_tier",
		"composio_toolkit_allowlist", "composio_toolkit_allowlist_intent",
		"future_unreviewed_field",
	}
	for _, actorSource := range []string{"task_token", "cloud_pat"} {
		for _, field := range deniedFields {
			t.Run(actorSource+"_"+field, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPut, "/api/agents/agent-1", nil)
				req.Header.Set("X-Actor-Source", actorSource)
				w := httptest.NewRecorder()
				if !rejectMachineAgentUpdate(w, req, map[string]json.RawMessage{field: json.RawMessage(`null`)}) {
					t.Fatal("non-descriptive field was not rejected")
				}
				if w.Code != http.StatusForbidden {
					t.Fatalf("status=%d, want 403", w.Code)
				}
			})
		}
	}

	req := httptest.NewRequest(http.MethodPut, "/api/agents/agent-1", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	w := httptest.NewRecorder()
	if rejectMachineAgentUpdate(w, req, map[string]json.RawMessage{
		"name":        json.RawMessage(`"safe name"`),
		"description": json.RawMessage(`"safe"`),
		"avatar_url":  json.RawMessage(`null`),
	}) {
		t.Fatal("exact descriptive allowlist was rejected")
	}
}

func TestMachineActorsCannotCreatePersistentAgents(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	for _, actorSource := range []string{"task_token", "cloud_pat"} {
		t.Run("generic_"+actorSource, func(t *testing.T) {
			name := "machine-create-denied-" + uuid.NewString()
			req := newRequest(http.MethodPost, "/api/agents", map[string]any{
				"name":       name,
				"runtime_id": handlerTestRuntimeID(t),
			})
			req.Header.Set("X-Actor-Source", actorSource)
			w := httptest.NewRecorder()
			testHandler.CreateAgent(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s, want 403", w.Code, w.Body.String())
			}
			var count int
			if err := testPool.QueryRow(context.Background(),
				`SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`,
				testWorkspaceID, name,
			).Scan(&count); err != nil {
				t.Fatalf("count created agents: %v", err)
			}
			if count != 0 {
				t.Fatalf("machine creation persisted %d agents", count)
			}
		})

		t.Run("template_"+actorSource, func(t *testing.T) {
			req := newRequest(http.MethodPost, "/api/agents/from-template", map[string]any{
				"name":          "machine-template-denied-" + uuid.NewString(),
				"runtime_id":    handlerTestRuntimeID(t),
				"template_slug": "coding",
			})
			req.Header.Set("X-Actor-Source", actorSource)
			w := httptest.NewRecorder()
			testHandler.CreateAgentFromTemplate(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s, want 403", w.Code, w.Body.String())
			}
		})
	}
}

func TestMachineActorsCannotMutateAgentControlFields(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	fields := []struct {
		name  string
		field string
		value any
	}{
		{"custom env", "custom_env", map[string]string{"TOKEN": "sentinel"}},
		{"custom args", "custom_args", []string{}},
		{"custom args intent", "custom_args_intent", "clear"},
		{"runtime config", "runtime_config", map[string]any{}},
		{"runtime config intent", "runtime_config_intent", "clear"},
		{"mcp config", "mcp_config", nil},
		{"mcp config intent", "mcp_config_intent", "clear"},
		{"composio allowlist", "composio_toolkit_allowlist", []string{}},
		{"composio allowlist intent", "composio_toolkit_allowlist_intent", "clear"},
		{"instructions", "instructions", "prompt sibling to exfiltrate"},
		{"runtime binding", "runtime_id", handlerTestRuntimeID(t)},
		{"runtime mode", "runtime_mode", "cloud"},
		{"runtime profile", "runtime_profile_id", uuid.NewString()},
		{"visibility", "visibility", "workspace"},
		{"permission mode", "permission_mode", "public_to"},
		{"invocation targets", "invocation_targets", []map[string]string{{"target_type": "workspace", "target_id": testWorkspaceID}}},
		{"owner", "owner_id", testUserID},
		{"access", "access", "public"},
		{"skills", "skills", []string{uuid.NewString()}},
		{"skill ids", "skill_ids", []string{uuid.NewString()}},
		{"disabled runtime skills", "disabled_runtime_skills", []string{"dangerous"}},
		{"max concurrency", "max_concurrent_tasks", 100},
		{"status", "status", "offline"},
		{"model", "model", "expensive-model"},
		{"thinking level", "thinking_level", "max"},
		{"service tier", "service_tier", "priority"},
	}
	for _, actorSource := range []string{"task_token", "cloud_pat"} {
		for _, field := range fields {
			t.Run("update_"+actorSource+"_"+field.name, func(t *testing.T) {
				agentID := createHandlerTestAgent(t, "machine-secret-update-"+uuid.NewString(), []byte(`{}`))
				const originalDescription = "original-safe-description"
				if _, err := testPool.Exec(context.Background(), `
					UPDATE agent SET description = $2 WHERE id = $1
				`, agentID, originalDescription); err != nil {
					t.Fatalf("seed description: %v", err)
				}
				req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
					"description": "machine-attempted-description",
					field.field:   field.value,
				}), "id", agentID)
				req.Header.Set("X-Actor-Source", actorSource)
				w := httptest.NewRecorder()
				testHandler.UpdateAgent(w, req)
				if w.Code != http.StatusForbidden {
					t.Fatalf("status=%d, want 403: %s", w.Code, w.Body.String())
				}
				var description string
				if err := testPool.QueryRow(context.Background(), `SELECT description FROM agent WHERE id = $1`, agentID).Scan(&description); err != nil {
					t.Fatalf("read description: %v", err)
				}
				if description != originalDescription {
					t.Fatalf("partial mutation landed: %q", description)
				}
			})
		}
	}
}

func TestMachineActorCanUpdateNonSecretAgentMetadata(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "machine-metadata-"+uuid.NewString(), []byte(`{}`))
	req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
		"description": "safe machine metadata",
	}), "id", agentID)
	req.Header.Set("X-Actor-Source", "task_token")
	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	var response map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode metadata response: %v", err)
	}
	if _, ok := response["custom_env"]; ok {
		t.Fatalf("metadata response exposed custom_env values: %v", response["custom_env"])
	}
}
