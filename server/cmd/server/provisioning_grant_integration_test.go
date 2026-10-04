package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCloudNodeTokenNeverOpensProvisioning(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/agents", strings.NewReader(`{"name":"cloud-should-not-create"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer mcn_not-a-real-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mcn_ router status=%d body=%s", resp.StatusCode, raw)
	}
}

func TestProvisioningGrantRouteChain(t *testing.T) {
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var fixtureAgent, runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id
		FROM agent a
		WHERE a.workspace_id = $1 AND a.name = 'Integration Test Agent'
	`, testWorkspaceID).Scan(&fixtureAgent, &runtimeID); err != nil {
		t.Fatalf("fixture agent: %v", err)
	}

	adminEmail := "prov-admin-" + suffix + "@example.com"
	memberEmail := "prov-member-" + suffix + "@example.com"
	otherSlug := "prov-other-" + suffix
	var taskIDs []string
	t.Cleanup(func() {
		c := context.Background()
		_, _ = testPool.Exec(c, `DELETE FROM agent_provisioning_grant WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(c, `DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'prov-%'`, testWorkspaceID)
		_, _ = testPool.Exec(c, `DELETE FROM skill WHERE workspace_id = $1 AND name LIKE 'prov-%'`, testWorkspaceID)
		_, _ = testPool.Exec(c, `DELETE FROM squad WHERE workspace_id = $1 AND name LIKE 'prov-%'`, testWorkspaceID)
		_, _ = testPool.Exec(c, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'prov-%'`, testWorkspaceID)
		for _, id := range taskIDs {
			_, _ = testPool.Exec(c, `DELETE FROM agent_task_queue WHERE id = $1`, id)
		}
		_, _ = testPool.Exec(c, `DELETE FROM workspace WHERE slug = $1`, otherSlug)
		_, _ = testPool.Exec(c, `DELETE FROM "user" WHERE email = $1 OR email = $2`, adminEmail, memberEmail)
	})

	adminID := provInsertUser(t, "Prov Admin "+suffix, adminEmail)
	memberID := provInsertUser(t, "Prov Member "+suffix, memberEmail)
	provExec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`, testWorkspaceID, adminID)
	provExec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, testWorkspaceID, memberID)

	managedID := provInsertAgent(t, "prov-managed-"+suffix, runtimeID, testUserID)
	bystanderID := provInsertAgent(t, "prov-bystander-"+suffix, runtimeID, testUserID)
	memberAgentID := provInsertAgent(t, "prov-member-agent-"+suffix, runtimeID, memberID)
	racerID := provInsertAgent(t, "prov-racer-"+suffix, runtimeID, testUserID)
	skillA := provInsertSkill(t, "prov-skill-a-"+suffix)
	skillB := provInsertSkill(t, "prov-skill-b-"+suffix)
	skillDenied := provInsertSkill(t, "prov-skill-no-"+suffix)
	squadA := provInsertSquad(t, "prov-squad-a-"+suffix, fixtureAgent)
	squadB := provInsertSquad(t, "prov-squad-b-"+suffix, fixtureAgent)
	otherRuntime := provInsertRuntime(t, testWorkspaceID, "prov-other-runtime-"+suffix, "public", testUserID)
	privateRuntime := provInsertRuntime(t, testWorkspaceID, "prov-private-runtime-"+suffix, "private", testUserID)

	var otherWorkspace, foreignRuntime string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description) VALUES ($1, $2, '') RETURNING id
	`, "Prov Other "+suffix, otherSlug).Scan(&otherWorkspace); err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	foreignRuntime = provInsertRuntime(t, otherWorkspace, "prov-foreign-runtime-"+suffix, "public", testUserID)

	goodToken, goodTask := provMintToken(t, fixtureAgent, runtimeID, testUserID, testUserID, "running")
	taskIDs = append(taskIDs, goodTask)
	ungrantedToken, ungrantedTask := provMintToken(t, bystanderID, runtimeID, testUserID, testUserID, "running")
	taskIDs = append(taskIDs, ungrantedTask)
	missingToken, missingTask := provMintToken(t, fixtureAgent, runtimeID, testUserID, "", "running")
	taskIDs = append(taskIDs, missingTask)
	deniedOriginToken, deniedOriginTask := provMintToken(t, fixtureAgent, runtimeID, testUserID, memberID, "running")
	taskIDs = append(taskIDs, deniedOriginTask)
	completedToken, completedTask := provMintToken(t, fixtureAgent, runtimeID, testUserID, testUserID, "completed")
	taskIDs = append(taskIDs, completedTask)
	memberToken, memberTask := provMintToken(t, memberAgentID, runtimeID, memberID, memberID, "running")
	taskIDs = append(taskIDs, memberTask)
	racerToken, racerTask := provMintToken(t, racerID, runtimeID, testUserID, testUserID, "running")
	taskIDs = append(taskIDs, racerTask)

	adminToken, err := generateTestJWT(adminID, adminEmail, "Prov Admin")
	if err != nil {
		t.Fatal(err)
	}

	baseGrant := func(agentID string, runtimes []map[string]any, maxNew int32) map[string]any {
		return map[string]any{
			"agent_id":             agentID,
			"max_new_agents":       maxNew,
			"max_concurrent_tasks": 1,
			"invocation_policy":    "workspace",
			"runtimes":             runtimes,
			"skill_ids":            []string{skillA, skillB},
			"managed_agent_ids":    []string{managedID},
			"squad_ids":            []string{squadA},
			"originator_user_ids":  []string{testUserID},
		}
	}
	allowedRuntime := []map[string]any{{"runtime_id": runtimeID, "models": []string{"gpt-6-astra"}}}

	code, raw := provCall(t, adminToken, http.MethodPost, "/api/agent-provisioning-grants", baseGrant(fixtureAgent, allowedRuntime, 1), nil)
	provRequire(t, code, raw, http.StatusForbidden, "insufficient permissions")

	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agent-provisioning-grants", baseGrant(fixtureAgent, allowedRuntime, 1), nil)
	provRequire(t, code, raw, http.StatusForbidden, "this endpoint is only available to human actors")

	code, raw = provCall(t, ungrantedToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-ungranted-create-" + suffix, "runtime_id": runtimeID, "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "this endpoint is only available to human actors")
	if provCount(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, "prov-ungranted-create-"+suffix) != 0 {
		t.Fatal("ungranted create persisted an agent")
	}

	code, raw = provCall(t, ungrantedToken, http.MethodPut, "/api/agents/"+bystanderID, map[string]any{
		"name": "prov-bystander-renamed-" + suffix,
	}, nil)
	provRequire(t, code, raw, http.StatusOK, "")

	code, raw = provCall(t, testToken, http.MethodPost, "/api/agent-provisioning-grants", baseGrant(fixtureAgent, []map[string]any{
		{"runtime_id": foreignRuntime, "models": []string{"gpt-6-astra"}},
	}, 1), nil)
	provRequire(t, code, raw, http.StatusBadRequest, "runtime does not belong to this workspace")

	code, raw = provCall(t, testToken, http.MethodPost, "/api/agent-provisioning-grants", baseGrant(fixtureAgent, allowedRuntime, 1), nil)
	provRequire(t, code, raw, http.StatusCreated, "")
	grantID := provID(t, raw)
	code, raw = provCall(t, testToken, http.MethodGet, "/api/agent-provisioning-grants?agent_id="+fixtureAgent, nil, nil)
	provRequire(t, code, raw, http.StatusOK, "")
	if provID(t, raw) != grantID {
		t.Fatalf("inspect id = %s, want %s", provID(t, raw), grantID)
	}
	code, raw = provCall(t, goodToken, http.MethodGet, "/api/agent-provisioning-grants?agent_id="+fixtureAgent, nil, nil)
	provRequire(t, code, raw, http.StatusForbidden, "this endpoint is only available to human actors")

	provStampedCloudDenied(t, fixtureAgent, goodTask)

	code, raw = provCall(t, goodToken, http.MethodPut, "/api/agents/"+managedID, map[string]any{
		"description": "prov-managed-ok",
	}, map[string]string{"X-Task-ID": deniedOriginTask, "X-Workspace-ID": otherWorkspace})
	provRequire(t, code, raw, http.StatusOK, "")

	code, raw = provCall(t, deniedOriginToken, http.MethodPut, "/api/agents/"+managedID, map[string]any{
		"description": "prov-should-not-apply",
	}, map[string]string{"X-Task-ID": goodTask})
	provRequire(t, code, raw, http.StatusForbidden, "originating human is not allowed by the provisioning grant")

	code, raw = provCall(t, missingToken, http.MethodPut, "/api/agents/"+managedID, map[string]any{
		"description": "prov-missing-origin",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "provisioning task has no originating human")

	code, raw = provCall(t, completedToken, http.MethodPut, "/api/agents/"+managedID, map[string]any{
		"description": "prov-completed",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "provisioning task does not match the grant")

	var managedDescription string
	if err := testPool.QueryRow(ctx, `SELECT description FROM agent WHERE id = $1`, managedID).Scan(&managedDescription); err != nil {
		t.Fatal(err)
	}
	if managedDescription != "prov-managed-ok" {
		t.Fatalf("managed description = %q", managedDescription)
	}

	createdName := "prov-created-" + suffix
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name":                 createdName,
		"description":          "catalog",
		"instructions":         "do the specialist job",
		"runtime_id":           runtimeID,
		"model":                "gpt-6-astra",
		"thinking_level":       "",
		"visibility":           "workspace",
		"max_concurrent_tasks": 1,
		"skill_ids":            []string{skillA},
	}, map[string]string{"X-Workspace-ID": otherWorkspace})
	provRequire(t, code, raw, http.StatusCreated, "")
	createdID := provID(t, raw)
	var gotWorkspace, gotModel, gotVisibility string
	var gotConcurrent int
	if err := testPool.QueryRow(ctx, `
		SELECT workspace_id, COALESCE(model, ''), visibility, max_concurrent_tasks
		FROM agent WHERE id = $1
	`, createdID).Scan(&gotWorkspace, &gotModel, &gotVisibility, &gotConcurrent); err != nil {
		t.Fatal(err)
	}
	if gotWorkspace != testWorkspaceID || gotModel != "gpt-6-astra" || gotVisibility != "workspace" || gotConcurrent != 1 {
		t.Fatalf("created agent workspace=%s model=%s visibility=%s concurrent=%d", gotWorkspace, gotModel, gotVisibility, gotConcurrent)
	}
	if provCount(t, `SELECT count(*) FROM agent_skill WHERE agent_id = $1 AND skill_id = $2`, createdID, skillA) != 1 {
		t.Fatal("create did not bind the allowed skill")
	}

	code, raw = provCall(t, goodToken, http.MethodPut, "/api/agents/"+createdID, map[string]any{
		"instructions": "updated specialist contract",
	}, nil)
	provRequire(t, code, raw, http.StatusOK, "")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents/"+createdID+"/skills/add", map[string]any{
		"skill_ids": []string{skillB},
	}, nil)
	provRequire(t, code, raw, http.StatusOK, "")
	code, raw = provCall(t, goodToken, http.MethodPut, "/api/agents/"+createdID+"/skills", map[string]any{
		"skill_ids": []string{skillA, skillB},
	}, nil)
	provRequire(t, code, raw, http.StatusOK, "")

	code, raw = provCall(t, goodToken, http.MethodPost, "/api/squads/"+squadA+"/members", map[string]any{
		"member_type": "agent", "member_id": createdID, "role": "leader",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "provisioning grants may only add squad members with role member")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/squads/"+squadA+"/members", map[string]any{
		"member_type": "member", "member_id": testUserID, "role": "member",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "provisioning grants cannot add workspace members to a squad")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/squads/"+squadA+"/members", map[string]any{
		"member_type": "agent", "member_id": fixtureAgent, "role": "member",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "cannot modify the provisioning agent")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/squads/"+squadB+"/members", map[string]any{
		"member_type": "agent", "member_id": createdID, "role": "member",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "squad is not allowed by the provisioning grant")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/squads/"+squadA+"/members", map[string]any{
		"member_type": "agent", "member_id": createdID, "role": "member",
	}, nil)
	provRequire(t, code, raw, http.StatusCreated, "")
	if provCount(t, `SELECT count(*) FROM squad_member WHERE squad_id = $1 AND member_type = 'agent' AND member_id = $2 AND role = 'member'`, squadA, createdID) != 1 {
		t.Fatal("allowed squad member was not added")
	}
	if provCount(t, `SELECT count(*) FROM squad_member WHERE squad_id = $1 AND member_id = $2`, squadA, testUserID) != 0 {
		t.Fatal("human member was added")
	}

	code, raw = provCall(t, goodToken, http.MethodGet, "/api/runtimes", nil, nil)
	provRequire(t, code, raw, http.StatusOK, "")
	var runtimes []map[string]any
	if err := json.Unmarshal(raw, &runtimes); err != nil {
		t.Fatalf("runtime list: %v body=%s", err, raw)
	}
	seenRuntime := map[string]bool{}
	for _, row := range runtimes {
		for key := range row {
			switch key {
			case "id", "workspace_id", "name", "provider", "runtime_mode", "status", "visibility":
			default:
				t.Fatalf("runtime catalog leaked %q", key)
			}
		}
		if id, _ := row["id"].(string); id != "" {
			seenRuntime[id] = true
		}
	}
	if !seenRuntime[runtimeID] || seenRuntime[otherRuntime] || seenRuntime[privateRuntime] {
		t.Fatalf("runtime catalog ids = %#v", seenRuntime)
	}

	secretName := "prov-secret-" + suffix
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": secretName, "runtime_id": runtimeID, "model": "gpt-6-astra",
		"max_concurrent_tasks": 1, "instructions": "PROV_INSTRUCTION_LEAK",
		"custom_env": map[string]string{"TOKEN": "PROV_SECRET_SHOULD_NOT_LEAK"},
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "field custom_env is forbidden under a provisioning grant")
	if provCount(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, secretName) != 0 {
		t.Fatal("forbidden create persisted an agent")
	}

	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-bad-runtime-" + suffix, "runtime_id": otherRuntime, "model": "gpt-6-astra", "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "runtime is not allowed by the provisioning grant")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-bad-model-" + suffix, "runtime_id": runtimeID, "model": "secret-model-zz", "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, `model "secret-model-zz" is not allowed by the provisioning grant`)
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-bad-skill-" + suffix, "runtime_id": runtimeID, "model": "gpt-6-astra",
		"max_concurrent_tasks": 1, "skill_ids": []string{skillDenied},
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "skill is not allowed by the provisioning grant")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-foreign-agent-" + suffix, "runtime_id": foreignRuntime, "model": "gpt-6-astra", "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusBadRequest, "invalid runtime_id")

	code, raw = provCall(t, goodToken, http.MethodPut, "/api/agents/"+fixtureAgent, map[string]any{
		"instructions": "PROV_INSTRUCTION_LEAK",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "cannot modify the provisioning agent")
	code, raw = provCall(t, goodToken, http.MethodPut, "/api/agents/"+bystanderID, map[string]any{
		"name": "prov-bystander-blocked-" + suffix,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "agent is not managed by the provisioning grant")
	var bystanderName string
	if err := testPool.QueryRow(ctx, `SELECT name FROM agent WHERE id = $1`, bystanderID).Scan(&bystanderName); err != nil {
		t.Fatal(err)
	}
	if bystanderName != "prov-bystander-renamed-"+suffix {
		t.Fatalf("ungranted rename was overwritten: %s", bystanderName)
	}

	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-over-limit-" + suffix, "runtime_id": runtimeID, "model": "gpt-6-astra", "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "new agent limit reached")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-default-concurrency-" + suffix, "runtime_id": runtimeID, "model": "gpt-6-astra",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "max_concurrent_tasks exceeds the provisioning grant")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-team-" + suffix, "runtime_id": runtimeID, "model": "gpt-6-astra", "max_concurrent_tasks": 1,
		"permission_mode":    "public_to",
		"invocation_targets": []map[string]string{{"target_type": "team", "target_id": "11111111-1111-1111-1111-111111111111"}},
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "invocation policy exceeds the provisioning grant")
	if provCount(t, `SELECT new_agents_created FROM agent_provisioning_grant WHERE id = $1`, grantID) != 1 {
		t.Fatal("denied creates changed the new-agent counter")
	}

	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents/from-template", map[string]any{
		"name": "prov-template-" + suffix, "runtime_id": runtimeID, "template_slug": "coding",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "this endpoint is only available to human actors")
	code, raw = provCall(t, goodToken, http.MethodPut, "/api/agents/"+createdID+"/skills/"+skillA+"/enabled", map[string]any{"enabled": false}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "this endpoint is only available to human actors")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents/"+createdID+"/archive", nil, nil)
	provRequire(t, code, raw, http.StatusForbidden, "this endpoint is only available to human actors")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/squads", map[string]any{
		"name": "prov-squad-new-" + suffix, "leader_id": fixtureAgent,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "machine actors may not change squad structure")

	code, raw = provCall(t, testToken, http.MethodPost, "/api/agent-provisioning-grants", map[string]any{
		"agent_id": memberAgentID, "max_new_agents": 1, "max_concurrent_tasks": 1,
		"invocation_policy": "private",
		"runtimes":          []map[string]any{{"runtime_id": privateRuntime, "models": []string{""}}},
		"skill_ids":         []string{}, "managed_agent_ids": []string{}, "squad_ids": []string{},
		"originator_user_ids": []string{memberID},
	}, nil)
	provRequire(t, code, raw, http.StatusCreated, "")
	code, raw = provCall(t, memberToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-private-runtime-" + suffix, "runtime_id": privateRuntime, "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "this runtime is private; only its owner or a workspace admin can create agents on it")
	if provCount(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, "prov-private-runtime-"+suffix) != 0 {
		t.Fatal("private runtime denial persisted an agent")
	}

	provConcurrentRevoke(t, racerID, racerToken, runtimeID, suffix)

	provExec(t, `UPDATE agent_provisioning_grant SET expires_at = now() - interval '1 minute' WHERE id = $1`, grantID)
	code, raw = provCall(t, goodToken, http.MethodPut, "/api/agents/"+createdID, map[string]any{
		"instructions": "expired should not apply",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "provisioning grant is expired")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-expired-" + suffix, "runtime_id": runtimeID, "model": "gpt-6-astra", "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "provisioning grant is expired")
	code, raw = provCall(t, testToken, http.MethodPost, "/api/agent-provisioning-grants", baseGrant(fixtureAgent, allowedRuntime, 1), nil)
	provRequire(t, code, raw, http.StatusConflict, "an active provisioning grant already exists for this agent; revoke it before creating another")

	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agent-provisioning-grants/"+grantID+"/revoke", map[string]any{}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "this endpoint is only available to human actors")
	code, raw = provCall(t, testToken, http.MethodPost, "/api/agent-provisioning-grants/"+grantID+"/revoke", map[string]any{}, nil)
	provRequire(t, code, raw, http.StatusOK, "")
	code, raw = provCall(t, goodToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-revoked-" + suffix, "runtime_id": runtimeID, "model": "gpt-6-astra", "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "this endpoint is only available to human actors")
	code, raw = provCall(t, goodToken, http.MethodPut, "/api/agents/"+createdID, map[string]any{
		"instructions": "revoked should not apply",
	}, nil)
	provRequire(t, code, raw, http.StatusForbidden, "no active provisioning grant for this agent")

	code, raw = provCall(t, testToken, http.MethodPost, "/api/agents", map[string]any{
		"name": "prov-human-" + suffix, "runtime_id": runtimeID, "max_concurrent_tasks": 1,
	}, nil)
	provRequire(t, code, raw, http.StatusCreated, "")

	var fixtureName string
	if err := testPool.QueryRow(ctx, `SELECT name FROM agent WHERE id = $1`, fixtureAgent).Scan(&fixtureName); err != nil {
		t.Fatal(err)
	}
	if fixtureName != "Integration Test Agent" {
		t.Fatalf("fixture agent was renamed to %q", fixtureName)
	}
	auditText := provAuditText(t)
	for _, want := range []string{"forbidden_field:custom_env", "model_not_allowed", "create_grant", "create_agent"} {
		if !strings.Contains(auditText, want) {
			t.Errorf("audit missing %q\n%s", want, auditText)
		}
	}
	for _, forbidden := range []string{"PROV_SECRET_SHOULD_NOT_LEAK", "PROV_INSTRUCTION_LEAK", "secret-model-zz"} {
		if strings.Contains(auditText, forbidden) {
			t.Errorf("audit contains %q", forbidden)
		}
	}
	if provCount(t, `SELECT count(*) FROM agent WHERE custom_env::text LIKE '%PROV_SECRET_SHOULD_NOT_LEAK%'`) != 0 {
		t.Fatal("secret was stored on an agent")
	}
}

func provConcurrentRevoke(t *testing.T, agentID, token, runtimeID, suffix string) {
	t.Helper()
	code, raw := provCall(t, testToken, http.MethodPost, "/api/agent-provisioning-grants", map[string]any{
		"agent_id": agentID, "max_new_agents": 1, "max_concurrent_tasks": 1,
		"invocation_policy": "private",
		"runtimes":          []map[string]any{{"runtime_id": runtimeID, "models": []string{"gpt-6-astra"}}},
		"skill_ids":         []string{}, "managed_agent_ids": []string{}, "squad_ids": []string{},
		"originator_user_ids": []string{testUserID},
	}, nil)
	provRequire(t, code, raw, http.StatusCreated, "")
	grantID := provID(t, raw)

	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var locked string
	if err := tx.QueryRow(context.Background(), `
		SELECT id FROM agent_provisioning_grant WHERE id = $1 FOR UPDATE
	`, grantID).Scan(&locked); err != nil {
		t.Fatal(err)
	}

	type result struct {
		code int
		raw  []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, raw, err := provCallResult(token, http.MethodPost, "/api/agents", map[string]any{
			"name": "prov-racer-child-" + suffix, "runtime_id": runtimeID,
			"model": "gpt-6-astra", "max_concurrent_tasks": 1,
		})
		done <- result{code, raw, err}
	}()
	select {
	case res := <-done:
		t.Fatalf("create returned while the grant lock was held: %d %s %v", res.code, res.raw, res.err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := tx.Exec(context.Background(), `
		UPDATE agent_provisioning_grant
		SET status = 'revoked', revoked_at = now(), revoked_by = $2
		WHERE id = $1
	`, grantID, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatal(res.err)
		}
		provRequire(t, res.code, res.raw, http.StatusForbidden, "no active provisioning grant for this agent")
	case <-time.After(10 * time.Second):
		t.Fatal("create did not return after the grant was revoked")
	}
	if provCount(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, "prov-racer-child-"+suffix) != 0 {
		t.Fatal("revoked race persisted an agent")
	}
}

func provStampedCloudDenied(t *testing.T, agentID, taskID string) {
	t.Helper()
	next := requireHumanOnSensitiveRoutesWithQueries(db.New(testPool))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/agents", strings.NewReader(`{"name":"cloud-bypass"}`))
	req.Header.Set("X-Actor-Source", "cloud_pat")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rr := httptest.NewRecorder()
	next.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("stamped cloud_pat status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "this endpoint is only available to human actors") {
		t.Fatalf("stamped cloud_pat body=%s", rr.Body.String())
	}
}

func provCall(t *testing.T, token, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	code, raw, err := provCallResult(token, method, path, body, headers)
	if err != nil {
		t.Fatal(err)
	}
	return code, raw
}

func provCallResult(token, method, path string, body any, headers ...map[string]string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, testServer.URL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	for _, hdr := range headers {
		for key, value := range hdr {
			req.Header.Set(key, value)
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

func provRequire(t *testing.T, code int, raw []byte, want int, wantErr string) {
	t.Helper()
	if code != want {
		t.Fatalf("status %d, want %d: %s", code, want, raw)
	}
	if wantErr == "" {
		return
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body: %v raw=%s", err, raw)
	}
	if body.Error != wantErr {
		t.Fatalf("error %q, want %q", body.Error, wantErr)
	}
}

func provID(t *testing.T, raw []byte) string {
	t.Helper()
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.ID == "" {
		t.Fatalf("response id: %v raw=%s", err, raw)
	}
	return body.ID
}

func provExec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func provCount(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func provInsertUser(t *testing.T, name, email string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, name, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func provInsertAgent(t *testing.T, name, runtimeID, ownerID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
		RETURNING id
	`, testWorkspaceID, name, runtimeID, ownerID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func provInsertSkill(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO skill (workspace_id, name, description, content, created_by)
		VALUES ($1, $2, '', '', $3) RETURNING id
	`, testWorkspaceID, name, testUserID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func provInsertSquad(t *testing.T, name, leaderID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO squad (workspace_id, name, leader_id, creator_id)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, testWorkspaceID, name, leaderID, testUserID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func provInsertRuntime(t *testing.T, workspaceID, name, visibility, ownerID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		) VALUES ($1, NULL, $2, 'cloud', 'integration_test_runtime', 'online', '', '{}'::jsonb, now(), $3, $4)
		RETURNING id
	`, workspaceID, name, visibility, ownerID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func provMintToken(t *testing.T, agentID, runtimeID, userID, originator, status string) (string, string) {
	t.Helper()
	var taskID string
	var origin any
	var accountable any
	if originator != "" {
		origin = originator
		accountable = originator
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, originator_user_id, accountable_user_id)
		VALUES ($1, $2, $3, 0, $4, $5) RETURNING id
	`, agentID, runtimeID, status, origin, accountable).Scan(&taskID); err != nil {
		t.Fatalf("task: %v", err)
	}
	raw, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + interval '2 hours')
	`, auth.HashToken(raw), taskID, agentID, testWorkspaceID, userID); err != nil {
		t.Fatalf("token: %v", err)
	}
	return raw, taskID
}

func provAuditText(t *testing.T) string {
	t.Helper()
	var text string
	if err := testPool.QueryRow(context.Background(), `
		SELECT coalesce(string_agg(
			action || ' ' || outcome || ' ' || reason || ' ' || coalesce(target_id, ''),
			E'\n' ORDER BY created_at
		), '')
		FROM agent_provisioning_audit
		WHERE workspace_id = $1
	`, testWorkspaceID).Scan(&text); err != nil {
		t.Fatal(err)
	}
	return text
}
