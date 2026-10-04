package main

// Independent review acceptance probe: provisioning grants constrain agents,
// not the existing human owner's authority. No implementation changes.
import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestVisionOwnerAuthoritySurvivesDelegation(t *testing.T) {
	suffix := fmt.Sprint(time.Now().UnixNano())
	runtime := provInsertRuntime(t, testWorkspaceID, "vision-owner-runtime-"+suffix, "public", testUserID)
	grantee := provInsertAgent(t, "vision-owner-grantee-"+suffix, runtime, testUserID)
	managed := provInsertAgent(t, "vision-owner-managed-"+suffix, runtime, testUserID)
	provExec(t, `UPDATE agent SET model='model-one' WHERE id=$1`, managed)
	t.Cleanup(func() {
		provDeleteProvisioning(testWorkspaceID)
		provExec(t, `DELETE FROM agent WHERE id=$1 OR id=$2`, grantee, managed)
		provExec(t, `DELETE FROM agent_runtime WHERE id=$1`, runtime)
	})
	code, raw := provCall(t, testToken, http.MethodPost, "/api/agent-provisioning-grants", map[string]any{
		"agent_id": grantee, "max_new_agents": 0, "max_concurrent_tasks": 1, "invocation_policy": "private",
		"runtimes":  []map[string]any{{"runtime_id": runtime, "models": []string{"model-one"}}},
		"skill_ids": []string{}, "managed_agent_ids": []string{managed}, "squad_ids": []string{}, "originator_user_ids": []string{testUserID},
	}, nil)
	provRequire(t, code, raw, http.StatusCreated, "")
	grant := provID(t, raw)
	for _, state := range []string{"active", "expired"} {
		if state == "expired" {
			provExec(t, `UPDATE agent_provisioning_grant SET expires_at=now()-interval '1 minute' WHERE id=$1`, grant)
		}
		code, raw = provCall(t, testToken, http.MethodPut, "/api/agents/"+managed, map[string]any{"model": "model-two"}, nil)
		if code != http.StatusOK {
			t.Errorf("human owner denied by %s delegated grant: status=%d body=%s", state, code, raw)
		}
	}
	var model string
	if err := testPool.QueryRow(context.Background(), `SELECT model FROM agent WHERE id=$1`, managed).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != "model-two" {
		t.Errorf("authorized human edit did not persist: model=%s", model)
	}
}
