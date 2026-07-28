package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestSanitizeMcpConfigForAgentResponse(t *testing.T) {
	const secret = "sentinel-mcp-secret-that-must-never-cross-the-agent-api"

	tests := []struct {
		name         string
		raw          string
		want         string
		wantRedacted bool
		wantNil      bool
	}{
		{name: "empty config", raw: `{}`, want: `{}`},
		{name: "empty server container", raw: `{"mcpServers":{}}`, want: `{"mcpServers":{}}`},
		{
			name:         "stdio masks values and identifier names",
			raw:          `{"mcpServers":{"` + secret + `":{"command":"node","args":["--token","` + secret + `"],"env":{"` + secret + `":"` + secret + `"},"type":"stdio"}}}`,
			want:         `{"mcpServers":{"server_1":{"command":"****","args":["****","****"],"env":{"env_1":"****"},"type":"stdio"}}}`,
			wantRedacted: true,
		},
		{
			name:         "remote masks URL headers and identifier names",
			raw:          `{"mcp_servers":{"` + secret + `":{"url":"https://example.invalid/?token=` + secret + `","headers":{"` + secret + `":"Bearer ` + secret + `"},"transport":"streamable-http"}}}`,
			want:         `{"mcp_servers":{"server_1":{"url":"****","headers":{"header_1":"****"},"transport":"streamable-http"}}}`,
			wantRedacted: true,
		},
		{name: "unknown root field fails closed", raw: `{"token":"` + secret + `"}`, wantRedacted: true, wantNil: true},
		{name: "unknown nested field fails closed", raw: `{"mcpServers":{"x":{"command":"node","token":"` + secret + `"}}}`, wantRedacted: true, wantNil: true},
		{name: "wrong nested value fails closed", raw: `{"mcpServers":{"x":{"command":"node","env":{"TOKEN":{"value":"` + secret + `"}}}}}`, wantRedacted: true, wantNil: true},
		{name: "duplicate key fails closed", raw: `{"mcpServers":{},"mcpServers":{"x":{"command":"` + secret + `"}}}`, wantRedacted: true, wantNil: true},
		{name: "malformed JSON fails closed", raw: `{"mcpServers":`, wantRedacted: true, wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, redacted := sanitizeMcpConfigForAgentResponse([]byte(tt.raw))
			if redacted != tt.wantRedacted {
				t.Fatalf("redacted = %v, want %v; body=%s", redacted, tt.wantRedacted, got)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("body = %s, want full suppression", got)
				}
				return
			}
			assertJSONEqual(t, got, tt.want)
			if bytes.Contains(got, []byte(secret)) {
				t.Fatalf("sanitized MCP config leaked sentinel secret: %s", got)
			}
		})
	}
}

func TestAgentToResponseRedactsEveryNonEmptyCustomArgsShape(t *testing.T) {
	const secret = "sentinel-custom-arg-secret"
	tests := map[string][]string{
		"ordinary":             {"--profile", "research"},
		"split api key":        {"--api-key", secret},
		"equal api key":        {"--api-key=" + secret},
		"split token":          {"--token", secret},
		"equal token":          {"--token=" + secret},
		"split auth":           {"--auth", secret},
		"equal auth":           {"--auth=" + secret},
		"split authorization":  {"--header", "Authorization: Bearer " + secret},
		"equal authorization":  {"--header=Authorization: Bearer " + secret},
		"split arbitrary name": {"--not-secret-looking", secret},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			resp := agentToResponse(db.Agent{CustomArgs: raw})
			encoded, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			if bytes.Contains(encoded, []byte(secret)) {
				t.Fatalf("custom_args leaked sentinel: %s", encoded)
			}
			if len(resp.CustomArgs) != 0 || resp.CustomArgsCount != len(args) || !resp.CustomArgsRedacted {
				t.Fatalf("custom_args metadata = %#v count=%d redacted=%v", resp.CustomArgs, resp.CustomArgsCount, resp.CustomArgsRedacted)
			}
		})
	}
}

func TestAgentToResponseIsTheServerOwnedSecretBoundary(t *testing.T) {
	const secret = "sentinel-central-agent-response-secret"
	customArgs, err := json.Marshal([]string{
		`-cmcp_servers.` + secret + `.env.TOKEN="` + secret + `"`,
	})
	if err != nil {
		t.Fatalf("marshal custom args: %v", err)
	}

	agent := db.Agent{
		CustomArgs: customArgs,
		RuntimeConfig: []byte(
			`{"mode":"gateway","gateway":{"token":"` + secret + `"},"Authorization":"Bearer ` + secret + `"}`,
		),
		McpConfig: []byte(
			`{"mcpServers":{"private":{"command":"node","env":{"TOKEN":"` + secret + `"}}}}`,
		),
	}
	resp := agentToResponse(agent)
	for name, candidate := range map[string]any{
		"list":                []AgentResponse{resp},
		"get":                 resp,
		"create":              resp,
		"update":              resp,
		"archive":             resp,
		"restore":             resp,
		"websocket broadcast": broadcastAgentResponse(resp),
	} {
		encoded, err := json.Marshal(candidate)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("%s leaked sentinel secret: %s", name, encoded)
		}
	}
	if !resp.McpConfigRedacted {
		t.Fatal("generic response must report mcp_config_redacted")
	}
	if !resp.CustomArgsRedacted {
		t.Fatal("generic response must report custom_args_redacted")
	}
	if !resp.RuntimeConfigRedacted {
		t.Fatal("generic response must report runtime_config_redacted")
	}
	if len(resp.CustomArgs) != 0 {
		t.Fatalf("generic response custom_args = %#v, want empty", resp.CustomArgs)
	}
	if !bytes.Contains(agent.McpConfig, []byte(secret)) ||
		!bytes.Contains(agent.CustomArgs, []byte(secret)) ||
		!bytes.Contains(agent.RuntimeConfig, []byte(secret)) {
		t.Fatal("response sanitization must not mutate the raw db.Agent consumed by daemon claim paths")
	}
}

func TestBroadcastAgentResponseFailsClosedForDirectRawConstruction(t *testing.T) {
	const secret = "sentinel-direct-broadcast-secret"
	projected := broadcastAgentResponse(AgentResponse{
		CustomArgs:               []string{"--token", secret},
		McpConfig:                json.RawMessage(`{"token":"` + secret + `"}`),
		RuntimeConfig:            map[string]any{"api_key": secret},
		ComposioToolkitAllowlist: []string{secret},
	})
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("marshal broadcast projection: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("direct broadcast construction leaked sentinel: %s", encoded)
	}
	if len(projected.CustomArgs) != 0 ||
		projected.CustomArgsCount != 2 ||
		!projected.CustomArgsRedacted {
		t.Fatalf("custom args projection = %#v", projected)
	}
	if projected.McpConfig != nil || !projected.McpConfigRedacted {
		t.Fatalf("MCP projection = %#v", projected)
	}
	if !projected.RuntimeConfigRedacted {
		t.Fatalf("runtime config projection = %#v", projected)
	}
	if len(projected.ComposioToolkitAllowlist) != 0 ||
		!projected.ComposioToolkitAllowlistRedacted {
		t.Fatalf("allowlist projection = %#v", projected)
	}
}

func TestRuntimeHasActiveAgentsResponseUsesStrictProjection(t *testing.T) {
	const secret = "sentinel-runtime-delete-conflict-secret"
	body := runtimeHasActiveAgentsResponse([]db.Agent{{
		CustomArgs:               []byte(`["--token","` + secret + `"]`),
		McpConfig:                []byte(`{"mcpServers":{"private":{"command":"` + secret + `"}}}`),
		RuntimeConfig:            []byte(`{"api_key":"` + secret + `"}`),
		ComposioToolkitAllowlist: []string{secret},
	}})

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal runtime conflict response: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("runtime conflict response leaked sentinel: %s", encoded)
	}

	agents, ok := body["active_agents"].([]AgentResponse)
	if !ok || len(agents) != 1 {
		t.Fatalf("active_agents = %#v, want one projected agent", body["active_agents"])
	}
	projected := agents[0]
	if len(projected.CustomArgs) != 0 || !projected.CustomArgsRedacted {
		t.Fatalf("custom args projection = %#v", projected)
	}
	if projected.McpConfig != nil || !projected.McpConfigRedacted {
		t.Fatalf("MCP projection = %#v", projected)
	}
	if !projected.RuntimeConfigRedacted {
		t.Fatalf("runtime config projection = %#v", projected)
	}
	if len(projected.ComposioToolkitAllowlist) != 0 ||
		!projected.ComposioToolkitAllowlistRedacted {
		t.Fatalf("allowlist projection = %#v", projected)
	}
}

func TestAgentToResponseMalformedCustomArgsFailsClosed(t *testing.T) {
	const secret = "sentinel-partially-decoded-custom-arg-secret"
	resp := agentToResponse(db.Agent{
		CustomArgs: []byte(`["` + secret + `", {"unexpected":"shape"}]`),
	})
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("malformed custom_args leaked a partially decoded secret: %s", encoded)
	}
	if !resp.CustomArgsRedacted || len(resp.CustomArgs) != 0 {
		t.Fatalf("malformed custom_args response = %#v, want redacted empty placeholder", resp.CustomArgs)
	}
}

func TestMcpRedactionSentinelCannotRoundTripAsStoredConfig(t *testing.T) {
	if !mcpJSONContainsRedactionSentinel(json.RawMessage(
		`{"mcpServers":{"server_1":{"command":"****","env":{"env_1":"****"}}}}`,
	)) {
		t.Fatal("masked MCP response must be detected at the write boundary")
	}
	if mcpJSONContainsRedactionSentinel(json.RawMessage(
		`{"mcpServers":{"safe":{"command":"node","env":{"MODE":"production"}}}}`,
	)) {
		t.Fatal("complete explicit MCP input must remain writable")
	}
}

func TestAgentEnvUpdateResponseNeverContainsValues(t *testing.T) {
	const secret = "sentinel-env-update-secret-that-must-not-be-reflected"
	response := newAgentEnvUpdateResponse(
		"agent-1",
		map[string]string{"API_TOKEN": secret, "UNCHANGED": "another-private-value"},
		envAudit{
			added:     []string{"API_TOKEN"},
			changed:   []string{},
			removed:   []string{"OLD_TOKEN"},
			preserved: []string{"UNCHANGED"},
		},
	)

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal update confirmation: %v", err)
	}
	for _, value := range []string{secret, "another-private-value"} {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("env update confirmation leaked value %q: %s", value, encoded)
		}
	}
	if response.CustomEnvKeyCount != 2 || !response.HasCustomEnv {
		t.Fatalf("confirmation summary = %#v, want two configured keys", response)
	}
	if !reflect.DeepEqual(response.CustomEnv, map[string]string{
		"API_TOKEN": envSentinel,
		"UNCHANGED": envSentinel,
	}) {
		t.Fatalf("legacy compatibility map = %#v, want retained keys masked", response.CustomEnv)
	}
	if !reflect.DeepEqual(response.CustomEnvKeys, []string{"API_TOKEN", "UNCHANGED"}) {
		t.Fatalf("confirmation keys = %v", response.CustomEnvKeys)
	}
}

func TestRuntimeProfilePublicResponseRedactsFixedArgsButDaemonKeepsRaw(t *testing.T) {
	const secret = "sentinel-runtime-profile-fixed-arg-secret"
	profile := db.RuntimeProfile{
		FixedArgs: []byte(`["--token","` + secret + `","--profile=production"]`),
	}

	public := runtimeProfileToResponse(profile)
	publicJSON, err := json.Marshal(public)
	if err != nil {
		t.Fatalf("marshal public runtime profile: %v", err)
	}
	if bytes.Contains(publicJSON, []byte(secret)) {
		t.Fatalf("public runtime profile leaked fixed args: %s", publicJSON)
	}
	if len(public.FixedArgs) != 0 || public.FixedArgsCount != 3 || !public.FixedArgsRedacted {
		t.Fatalf("public fixed args = %#v count=%d redacted=%v", public.FixedArgs, public.FixedArgsCount, public.FixedArgsRedacted)
	}

	daemon := runtimeProfileToDaemonResponse(profile)
	if !reflect.DeepEqual(daemon.FixedArgs, []string{"--token", secret, "--profile=production"}) {
		t.Fatalf("daemon fixed args = %#v", daemon.FixedArgs)
	}
}

func TestRawDaemonAgentClaimConfigPreservesExecutionSecrets(t *testing.T) {
	const secret = "sentinel-daemon-execution-secret"
	agent := db.Agent{
		CustomEnv:     []byte(`{"TOKEN":"` + secret + `"}`),
		CustomArgs:    []byte(`["--token","` + secret + `"]`),
		McpConfig:     []byte(`{"mcpServers":{"private":{"command":"` + secret + `"}}}`),
		RuntimeConfig: []byte(`{"provider":{"api_key":"` + secret + `"}}`),
	}

	raw := rawAgentConfigForClaim(agent)
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal raw daemon config: %v", err)
	}
	if !bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("daemon execution config lost persisted values: %s", encoded)
	}

	public, err := json.Marshal(agentToResponse(agent))
	if err != nil {
		t.Fatalf("marshal public agent: %v", err)
	}
	if bytes.Contains(public, []byte(secret)) {
		t.Fatalf("public agent leaked daemon execution values: %s", public)
	}
}

func TestAgentEnvAuditDetailsNeverContainValues(t *testing.T) {
	const secret = "sentinel-agent-env-audit-secret"
	existing := map[string]string{"TOKEN": secret, "UNCHANGED": secret}
	_, audit := mergeAgentEnv(existing, map[string]string{
		"TOKEN":     "rotated-" + secret,
		"UNCHANGED": envSentinel,
		"ADDED":     secret,
	})
	details := newAgentEnvAuditDetails("agent-1", "safe-agent-name", audit)
	encoded, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("marshal audit details: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("env audit leaked a submitted or persisted value: %s", encoded)
	}
	for _, key := range []string{"TOKEN", "UNCHANGED", "ADDED"} {
		if !bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("env audit lost safe key name %q: %s", key, encoded)
		}
	}
}

func TestArchiveRestoreNonOwnerHTTPAndBroadcastProjectionsDoNotLeak(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)

	const secret = "sentinel-archive-restore-projection-secret"
	agentID, _ := allowlistFixture(t)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent
		SET custom_args = $2::jsonb,
		    mcp_config = $3::jsonb,
		    runtime_config = $4::jsonb,
		    composio_toolkit_allowlist = $5
		WHERE id = $1
	`, agentID,
		`["--token","`+secret+`"]`,
		`{"mcpServers":{"`+secret+`":{"command":"node","env":{"TOKEN":"`+secret+`"}}}}`,
		`{"mode":"gateway","gateway":{"host":"`+secret+`","token":"`+secret+`"},"api_key":"`+secret+`"}`,
		[]string{"private-" + secret},
	); err != nil {
		t.Fatalf("seed secret-bearing agent fields: %v", err)
	}

	var mu sync.Mutex
	var broadcasts []AgentResponse
	record := func(event events.Event) {
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			return
		}
		agent, ok := payload["agent"].(AgentResponse)
		if !ok || agent.ID != agentID {
			return
		}
		mu.Lock()
		broadcasts = append(broadcasts, agent)
		mu.Unlock()
	}
	testHandler.Bus.Subscribe(protocol.EventAgentArchived, record)
	testHandler.Bus.Subscribe(protocol.EventAgentRestored, record)

	assertSafe := func(label string, response AgentResponse, broadcast bool) {
		t.Helper()
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("%s marshal: %v", label, err)
		}
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("%s leaked sentinel: %s", label, encoded)
		}
		if len(response.CustomArgs) != 0 || !response.CustomArgsRedacted {
			t.Fatalf("%s custom args projection = %#v", label, response.CustomArgs)
		}
		if !response.RuntimeConfigRedacted {
			t.Fatalf("%s runtime config must be marked redacted: %#v", label, response.RuntimeConfig)
		}
		if len(response.ComposioToolkitAllowlist) != 0 ||
			!response.ComposioToolkitAllowlistRedacted {
			t.Fatalf("%s exposed non-owner allowlist: %#v", label, response.ComposioToolkitAllowlist)
		}
		if broadcast && (response.McpConfig != nil || !response.McpConfigRedacted) {
			t.Fatalf("%s broadcast MCP projection = %s", label, response.McpConfig)
		}
	}

	invoke := func(label, path string, handler http.HandlerFunc) {
		t.Helper()
		before := func() int {
			mu.Lock()
			defer mu.Unlock()
			return len(broadcasts)
		}()
		request := withURLParam(
			newRequest(http.MethodPost, path, nil),
			"id",
			agentID,
		)
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", label, recorder.Code, recorder.Body.String())
		}
		if bytes.Contains(recorder.Body.Bytes(), []byte(secret)) {
			t.Fatalf("%s HTTP body leaked sentinel: %s", label, recorder.Body.String())
		}
		var response AgentResponse
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("%s decode: %v", label, err)
		}
		assertSafe(label+" HTTP", response, false)

		mu.Lock()
		defer mu.Unlock()
		if len(broadcasts) != before+1 {
			t.Fatalf("%s broadcasts = %d after %d, want one", label, len(broadcasts), before)
		}
		assertSafe(label+" broadcast", broadcasts[len(broadcasts)-1], true)
	}

	// testUserID is the workspace owner but not this agent's owner, exercising
	// the admin projection rather than the privileged agent-owner response.
	invoke("archive", "/api/agents/"+agentID+"/archive", testHandler.ArchiveAgent)
	invoke("restore", "/api/agents/"+agentID+"/restore", testHandler.RestoreAgent)
}
