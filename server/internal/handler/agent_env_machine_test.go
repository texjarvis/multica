package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentEnvMachineActorsCannotReadOrMutateValues(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	const (
		originalSecret = "env-machine-original-secret"
		replacement    = "env-machine-replacement-secret"
	)
	agentID := createHandlerTestAgent(t, "env-machine-boundary-agent", nil)
	if _, err := testPool.Exec(
		context.Background(),
		`UPDATE agent SET custom_env = $1::jsonb WHERE id = $2`,
		`{"PRIVATE_TOKEN":"`+originalSecret+`"}`,
		agentID,
	); err != nil {
		t.Fatalf("seed custom_env: %v", err)
	}

	for _, actorSource := range []string{"task_token", "cloud_pat"} {
		t.Run(actorSource, func(t *testing.T) {
			getReq := newRequest(http.MethodGet, "/api/agents/"+agentID+"/env", nil)
			getReq = withURLParam(getReq, "id", agentID)
			getReq.Header.Set("X-Actor-Source", actorSource)
			getW := httptest.NewRecorder()
			testHandler.GetAgentEnv(getW, getReq)
			if getW.Code != http.StatusForbidden {
				t.Fatalf("GET status=%d body=%s, want 403", getW.Code, getW.Body.String())
			}
			if strings.Contains(getW.Body.String(), originalSecret) {
				t.Fatalf("GET error response leaked persisted env value: %s", getW.Body.String())
			}

			putReq := newRequest(http.MethodPut, "/api/agents/"+agentID+"/env", map[string]any{
				"custom_env": map[string]string{"PRIVATE_TOKEN": replacement},
			})
			putReq = withURLParam(putReq, "id", agentID)
			putReq.Header.Set("X-Actor-Source", actorSource)
			putW := httptest.NewRecorder()
			testHandler.UpdateAgentEnv(putW, putReq)
			if putW.Code != http.StatusForbidden {
				t.Fatalf("PUT status=%d body=%s, want 403", putW.Code, putW.Body.String())
			}
			if strings.Contains(putW.Body.String(), replacement) ||
				strings.Contains(putW.Body.String(), originalSecret) {
				t.Fatalf("PUT error response leaked env value: %s", putW.Body.String())
			}

			var raw []byte
			if err := testPool.QueryRow(
				context.Background(),
				`SELECT custom_env FROM agent WHERE id = $1`,
				agentID,
			).Scan(&raw); err != nil {
				t.Fatalf("read custom_env after blocked PUT: %v", err)
			}
			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decode stored custom_env: %v", err)
			}
			if got["PRIVATE_TOKEN"] != originalSecret || len(got) != 1 {
				t.Fatalf("blocked PUT changed custom_env: %#v", got)
			}
		})
	}
}
