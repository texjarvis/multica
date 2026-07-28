package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func fetchAgentCustomArgs(t *testing.T, agentID string) []string {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT custom_args FROM agent WHERE id = $1`,
		agentID,
	).Scan(&raw); err != nil {
		t.Fatalf("read stored custom_args: %v", err)
	}
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("decode stored custom_args: %v", err)
	}
	return args
}

func fetchAgentRuntimeConfig(t *testing.T, agentID string) []byte {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT runtime_config FROM agent WHERE id = $1`,
		agentID,
	).Scan(&raw); err != nil {
		t.Fatalf("read stored runtime_config: %v", err)
	}
	return raw
}

func TestUpdateAgentRuntimeConfigRequiresExplicitIntentOnceHidden(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Hidden Runtime Config Intent", nil)
	const stored = `{"mode":"gateway","gateway":{"host":"private.internal","token":"sentinel-runtime-token"},"provider_private":{"unknown":"keep-me"}}`
	seedStored := func() {
		t.Helper()
		if _, err := testPool.Exec(
			ctx,
			`UPDATE agent SET runtime_config = $2::jsonb WHERE id = $1`,
			agentID,
			stored,
		); err != nil {
			t.Fatalf("seed runtime_config: %v", err)
		}
	}
	seedStored()

	t.Run("empty public placeholder preserves hidden config", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"description":             "unrelated edit",
			"runtime_config":          map[string]any{},
			"runtime_config_redacted": true,
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertJSONEqual(t, fetchAgentRuntimeConfig(t, agentID), stored)
	})

	t.Run("marker-stripped replacement without intent rejects", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"runtime_config": map[string]any{"mode": "local"},
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		assertJSONEqual(t, fetchAgentRuntimeConfig(t, agentID), stored)
	})

	t.Run("intent without config rejects", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"runtime_config_intent": hiddenMutationIntentClear,
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		assertJSONEqual(t, fetchAgentRuntimeConfig(t, agentID), stored)
	})

	t.Run("replace intent rejects empty config", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"runtime_config":        map[string]any{},
			"runtime_config_intent": hiddenMutationIntentReplace,
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		assertJSONEqual(t, fetchAgentRuntimeConfig(t, agentID), stored)
	})

	t.Run("explicit replacement succeeds", func(t *testing.T) {
		const replacement = `{"mode":"local","fresh":{"enabled":true}}`
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"runtime_config": map[string]any{
				"mode": "local",
				"fresh": map[string]any{
					"enabled": true,
				},
			},
			"runtime_config_intent": hiddenMutationIntentReplace,
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertJSONEqual(t, fetchAgentRuntimeConfig(t, agentID), replacement)
	})

	t.Run("explicit clear succeeds", func(t *testing.T) {
		seedStored()
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"runtime_config":        map[string]any{},
			"runtime_config_intent": hiddenMutationIntentClear,
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertJSONEqual(t, fetchAgentRuntimeConfig(t, agentID), `{}`)
	})
}

func TestUpdateAgentCustomArgsRequiresIntentOnceHidden(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Hidden Args Intent", nil)
	const secret = "sentinel-stored-custom-arg"
	if _, err := testPool.Exec(ctx,
		`UPDATE agent SET custom_args = $2::jsonb WHERE id = $1`,
		agentID, `["--token","`+secret+`"]`,
	); err != nil {
		t.Fatalf("seed custom_args: %v", err)
	}

	t.Run("unrelated full response replay preserves empty placeholder", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"description":          "safe unrelated edit",
			"custom_args":          []string{},
			"custom_args_redacted": true,
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := fetchAgentCustomArgs(t, agentID); !reflect.DeepEqual(got, []string{"--token", secret}) {
			t.Fatalf("stored args = %v", got)
		}
	})

	t.Run("ambiguous legacy nonempty replacement rejects", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"custom_args": []string{"--profile", "legacy"},
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		if got := fetchAgentCustomArgs(t, agentID); !reflect.DeepEqual(got, []string{"--token", secret}) {
			t.Fatalf("ambiguous request changed stored args: %v", got)
		}
	})

	t.Run("explicit replacement succeeds", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"custom_args":        []string{"--profile", "fresh"},
			"custom_args_intent": hiddenMutationIntentReplace,
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := fetchAgentCustomArgs(t, agentID); !reflect.DeepEqual(got, []string{"--profile", "fresh"}) {
			t.Fatalf("stored args = %v", got)
		}
	})

	t.Run("explicit clear succeeds", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"custom_args":        []string{},
			"custom_args_intent": hiddenMutationIntentClear,
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := fetchAgentCustomArgs(t, agentID); len(got) != 0 {
			t.Fatalf("stored args = %v, want empty", got)
		}
	})
}

func TestUpdateAgentMcpConfigReplayPreservesAndAmbiguousReplaceRejects(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const stored = `{"mcpServers":{"private":{"command":"node","env":{"TOKEN":"persisted-secret"}}}}`
	agentID := createHandlerTestAgent(t, "Hidden MCP Intent", []byte(stored))

	for _, replay := range []map[string]any{
		{
			"description":         "null replay",
			"mcp_config":          nil,
			"mcp_config_redacted": true,
		},
		{
			"description": "masked replay",
			"mcp_config": map[string]any{
				"mcpServers": map[string]any{
					"server_1": map[string]any{"command": envSentinel},
				},
			},
			"mcp_config_redacted": true,
		},
	} {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, replay), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("replay status = %d: %s", w.Code, w.Body.String())
		}
		assertJSONEqual(t, fetchAgentMcpConfig(t, agentID), stored)
	}

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
		"mcp_config": map[string]any{
			"mcpServers": map[string]any{
				"replacement": map[string]any{"command": "legacy-node"},
			},
		},
	}), "id", agentID)
	testHandler.UpdateAgent(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("ambiguous replacement status = %d, want 409: %s", w.Code, w.Body.String())
	}
	assertJSONEqual(t, fetchAgentMcpConfig(t, agentID), stored)
}
