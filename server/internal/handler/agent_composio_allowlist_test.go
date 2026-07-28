package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// allowlistFixture seeds an agent owned by a freshly-created ordinary member.
// The package's testUserID remains the workspace owner, letting tests exercise
// both agent-owner and workspace-owner/admin authorization.
func allowlistFixture(t *testing.T) (agentID, agentOwnerID string) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Allowlist Owner', 'allowlist-owner@multica.test')
		RETURNING id
	`).Scan(&agentOwnerID); err != nil {
		t.Fatalf("create owner user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM "user" WHERE email = 'allowlist-owner@multica.test'`)
	})

	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, agentOwnerID); err != nil {
		t.Fatalf("add owner as workspace member: %v", err)
	}

	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args
		)
		VALUES ($1, 'allowlist-test-agent', '', 'cloud', '{}'::jsonb,
		        $2, 'workspace', 1, $3, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, handlerTestRuntimeID(t), agentOwnerID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM activity_log WHERE action IN ($1, $2) AND details->>'agent_id' = $3`,
			agentComposioAllowlistActivityRevealed, agentComposioAllowlistActivityUpdated, agentID)
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})
	return agentID, agentOwnerID
}

func readAllowlistColumn(t *testing.T, agentID string) ([]string, bool) {
	t.Helper()
	var stored []string
	var isNull bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT COALESCE(composio_toolkit_allowlist, '{}'::text[]),
		       composio_toolkit_allowlist IS NULL
		FROM agent WHERE id = $1
	`, agentID).Scan(&stored, &isNull); err != nil {
		t.Fatalf("read allowlist column: %v", err)
	}
	return stored, isNull
}

func seedAllowlist(t *testing.T, agentID string, slugs []string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET composio_toolkit_allowlist = $2 WHERE id = $1
	`, agentID, slugs); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}
}

func assertStoredAllowlist(t *testing.T, agentID string, want []string, wantNull bool) {
	t.Helper()
	got, isNull := readAllowlistColumn(t, agentID)
	if isNull != wantNull {
		t.Fatalf("allowlist NULL=%v, want %v (value=%v)", isNull, wantNull, got)
	}
	if len(got) != len(want) {
		t.Fatalf("allowlist=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allowlist[%d]=%q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func TestUpdateAgent_AllowlistRequiresExplicitIntent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, ownerID := allowlistFixture(t)
	seedAllowlist(t, agentID, []string{"notion"})

	// These are the destructive shapes emitted by old/default/full-response
	// clients. None may clear or replace a hidden authoritative list.
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"empty replay", []string{}},
		{"null replay", nil},
		{"legacy replacement", []string{"github"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.UpdateAgent(w, withURLParam(newRequestAs(
				ownerID, http.MethodPut, "/api/agents/"+agentID,
				map[string]any{
					"description":                         "safe unrelated edit",
					"composio_toolkit_allowlist":          tc.value,
					"composio_toolkit_allowlist_redacted": true,
				},
			), "id", agentID))
			if w.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s, want 409", w.Code, w.Body.String())
			}
			assertStoredAllowlist(t, agentID, []string{"notion"}, false)
		})
	}

	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"composio_toolkit_allowlist_intent": "clear"},
	), "id", agentID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("intent-without-value status=%d body=%s, want 400", w.Code, w.Body.String())
	}
	assertStoredAllowlist(t, agentID, []string{"notion"}, false)
}

func TestUpdateAgent_AllowlistExplicitReplaceAndClear(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, ownerID := allowlistFixture(t)

	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{
			"composio_toolkit_allowlist_intent": "replace",
			"composio_toolkit_allowlist":        []string{" Notion ", "NOTION", "github", ""},
		},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("replace status=%d body=%s", w.Code, w.Body.String())
	}
	assertStoredAllowlist(t, agentID, []string{"notion", "github"}, false)
	var resp AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.ComposioToolkitAllowlist) != 0 ||
		resp.ComposioToolkitAllowlistCount != 2 ||
		!resp.ComposioToolkitAllowlistRedacted {
		t.Fatalf("generic replace response was not value-free: %+v", resp)
	}

	w = httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{
			"composio_toolkit_allowlist_intent": "clear",
			"composio_toolkit_allowlist":        []string{},
		},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", w.Code, w.Body.String())
	}
	assertStoredAllowlist(t, agentID, nil, true)
}

func TestUpdateAgent_AllowlistIntentValidationFailsClosed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, ownerID := allowlistFixture(t)
	seedAllowlist(t, agentID, []string{"notion"})

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"unknown intent", map[string]any{
			"composio_toolkit_allowlist_intent": "merge",
			"composio_toolkit_allowlist":        []string{"github"},
		}},
		{"empty replacement", map[string]any{
			"composio_toolkit_allowlist_intent": "replace",
			"composio_toolkit_allowlist":        []string{},
		}},
		{"blank replacement", map[string]any{
			"composio_toolkit_allowlist_intent": "replace",
			"composio_toolkit_allowlist":        []string{" "},
		}},
		{"nonempty clear", map[string]any{
			"composio_toolkit_allowlist_intent": "clear",
			"composio_toolkit_allowlist":        []string{"github"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.UpdateAgent(w, withURLParam(newRequestAs(
				ownerID, http.MethodPut, "/api/agents/"+agentID, tc.body,
			), "id", agentID))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
			}
			assertStoredAllowlist(t, agentID, []string{"notion"}, false)
		})
	}
}

func TestUpdateAgent_AllowlistGenericWriteRejectedForNonOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, _ := allowlistFixture(t)
	seedAllowlist(t, agentID, []string{"notion"})

	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequest(
		http.MethodPut, "/api/agents/"+agentID,
		map[string]any{
			"composio_toolkit_allowlist_intent": "replace",
			"composio_toolkit_allowlist":        []string{"github"},
		},
	), "id", agentID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", w.Code, w.Body.String())
	}
	assertStoredAllowlist(t, agentID, []string{"notion"}, false)
}

func TestDedicatedAgentAllowlist_OwnerAndAdminCanReveal(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, ownerID := allowlistFixture(t)
	seedAllowlist(t, agentID, []string{"notion", "github"})

	for _, tc := range []struct {
		name   string
		userID string
	}{
		{"agent owner", ownerID},
		{"workspace owner", testUserID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.GetAgentComposioToolkitAllowlist(w, withURLParam(newRequestAs(
				tc.userID, http.MethodGet, "/api/agents/"+agentID+"/composio-toolkit-allowlist", nil,
			), "id", agentID))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var resp AgentComposioAllowlistResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.AgentID != agentID ||
				len(resp.ToolkitSlugs) != 2 ||
				resp.ToolkitSlugs[0] != "notion" ||
				resp.ToolkitSlugs[1] != "github" {
				t.Fatalf("response=%+v", resp)
			}
		})
	}

	var audits int
	if err := testPool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM activity_log
		WHERE action = $1 AND details->>'agent_id' = $2
	`, agentComposioAllowlistActivityRevealed, agentID).Scan(&audits); err != nil {
		t.Fatalf("count reveal audits: %v", err)
	}
	if audits != 2 {
		t.Fatalf("reveal audit count=%d, want 2", audits)
	}
}

func TestDedicatedAgentAllowlist_AdminCanAddRemoveAndClearWithoutEcho(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, _ := allowlistFixture(t)
	seedAllowlist(t, agentID, []string{"notion"})

	for _, tc := range []struct {
		name      string
		intent    string
		slugs     []string
		want      []string
		wantNull  bool
		wantCount int
	}{
		{"add", "replace", []string{"notion", "slack"}, []string{"notion", "slack"}, false, 2},
		{"remove", "replace", []string{"slack"}, []string{"slack"}, false, 1},
		{"clear", "clear", nil, nil, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"intent": tc.intent}
			if tc.slugs != nil {
				body["toolkit_slugs"] = tc.slugs
			}
			w := httptest.NewRecorder()
			testHandler.UpdateAgentComposioToolkitAllowlist(w, withURLParam(newRequest(
				http.MethodPut, "/api/agents/"+agentID+"/composio-toolkit-allowlist", body,
			), "id", agentID))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			assertStoredAllowlist(t, agentID, tc.want, tc.wantNull)
			var resp AgentComposioAllowlistUpdateResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.AgentID != agentID || resp.ToolkitCount != tc.wantCount {
				t.Fatalf("response=%+v, want agent/count %s/%d", resp, agentID, tc.wantCount)
			}
			for _, slug := range tc.slugs {
				if slug != "" && strings.Contains(w.Body.String(), slug) {
					t.Fatalf("value-free update response echoed slug %q: %s", slug, w.Body.String())
				}
			}
		})
	}

	var audits int
	if err := testPool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM activity_log
		WHERE action = $1 AND details->>'agent_id' = $2
	`, agentComposioAllowlistActivityUpdated, agentID).Scan(&audits); err != nil {
		t.Fatalf("count update audits: %v", err)
	}
	if audits != 3 {
		t.Fatalf("update audit count=%d, want 3", audits)
	}
}

func TestDedicatedAgentAllowlist_MachineActorsDenied(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, _ := allowlistFixture(t)
	seedAllowlist(t, agentID, []string{"private-toolkit"})

	for _, actorSource := range []string{"task_token", "cloud_pat"} {
		t.Run(actorSource, func(t *testing.T) {
			getReq := withURLParam(newRequest(
				http.MethodGet, "/api/agents/"+agentID+"/composio-toolkit-allowlist", nil,
			), "id", agentID)
			getReq.Header.Set("X-Actor-Source", actorSource)
			getW := httptest.NewRecorder()
			testHandler.GetAgentComposioToolkitAllowlist(getW, getReq)
			if getW.Code != http.StatusForbidden ||
				strings.Contains(getW.Body.String(), "private-toolkit") {
				t.Fatalf("GET status=%d body=%s, want value-free 403", getW.Code, getW.Body.String())
			}

			putReq := withURLParam(newRequest(
				http.MethodPut, "/api/agents/"+agentID+"/composio-toolkit-allowlist",
				map[string]any{"intent": "clear"},
			), "id", agentID)
			putReq.Header.Set("X-Actor-Source", actorSource)
			putW := httptest.NewRecorder()
			testHandler.UpdateAgentComposioToolkitAllowlist(putW, putReq)
			if putW.Code != http.StatusForbidden {
				t.Fatalf("PUT status=%d body=%s, want 403", putW.Code, putW.Body.String())
			}
			assertStoredAllowlist(t, agentID, []string{"private-toolkit"}, false)
		})
	}
}

func TestGetAgent_AllowlistIsAlwaysValueFree(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, ownerID := allowlistFixture(t)
	seedAllowlist(t, agentID, []string{"notion", "github"})

	for _, tc := range []struct {
		name   string
		userID string
	}{
		{"agent owner", ownerID},
		{"workspace owner", testUserID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.GetAgent(w, withURLParam(newRequestAs(
				tc.userID, http.MethodGet, "/api/agents/"+agentID, nil,
			), "id", agentID))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var resp AgentResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(resp.ComposioToolkitAllowlist) != 0 ||
				resp.ComposioToolkitAllowlistCount != 2 ||
				!resp.ComposioToolkitAllowlistRedacted {
				t.Fatalf("generic response leaked or lost metadata: %+v", resp)
			}
		})
	}
}

func TestAgentAllowlistSuppressedWhenComposioFlagDisabled(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, false)
	agentID, ownerID := allowlistFixture(t)
	seedAllowlist(t, agentID, []string{"notion"})

	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{
			"composio_toolkit_allowlist_intent": "replace",
			"composio_toolkit_allowlist":        []string{"github"},
		},
	), "id", agentID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("flag-off generic update status=%d body=%s, want 404", w.Code, w.Body.String())
	}
	assertStoredAllowlist(t, agentID, []string{"notion"}, false)

	w = httptest.NewRecorder()
	testHandler.GetAgentComposioToolkitAllowlist(w, withURLParam(newRequestAs(
		ownerID, http.MethodGet, "/api/agents/"+agentID+"/composio-toolkit-allowlist", nil,
	), "id", agentID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("flag-off dedicated GET status=%d body=%s, want 404", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	testHandler.GetAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodGet, "/api/agents/"+agentID, nil,
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("flag-off generic GET status=%d body=%s", w.Code, w.Body.String())
	}
	var resp AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.ComposioToolkitAllowlist) != 0 ||
		resp.ComposioToolkitAllowlistCount != 0 ||
		resp.ComposioToolkitAllowlistRedacted {
		t.Fatalf("flag-off generic response exposed allowlist state: %+v", resp)
	}
}

func TestNormaliseComposioToolkitAllowlist_PureFunction(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil-in nil-out", nil, nil},
		{"empty-in empty-out", []string{}, []string{}},
		{"trim", []string{"  notion  "}, []string{"notion"}},
		{"lower", []string{"NOTION", "GitHub"}, []string{"notion", "github"}},
		{"dedupe", []string{"notion", "NOTION", "notion"}, []string{"notion"}},
		{"drop empty", []string{"", "   ", "notion"}, []string{"notion"}},
		{"preserve order of first-seen", []string{"notion", "github", "notion"}, []string{"notion", "github"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normaliseComposioToolkitAllowlist(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %v; want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v; want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d]=%q; want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
