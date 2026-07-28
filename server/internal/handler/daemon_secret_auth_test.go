package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func createDaemonAuthMember(t *testing.T, role string) string {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	var userID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, "Daemon auth "+suffix, "daemon-auth-"+suffix+"@multica.test").Scan(&userID); err != nil {
		t.Fatalf("create daemon auth user: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, $3)
	`, testWorkspaceID, userID, role); err != nil {
		t.Fatalf("create daemon auth member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return userID
}

func createDaemonAuthRuntime(t *testing.T, ownerID, daemonID string, metadata map[string]any) string {
	t.Helper()
	if metadata == nil {
		metadata = map[string]any{}
	}
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal runtime metadata: %v", err)
	}
	provider := "daemon_auth_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', 'daemon auth fixture', $5, now(), 'private', $6)
		RETURNING id
	`, testWorkspaceID, daemonID, provider, provider, rawMetadata, ownerID).Scan(&runtimeID); err != nil {
		t.Fatalf("create daemon auth runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID
}

func seedDaemonAuthClaim(t *testing.T, runtimeID, label string) string {
	t.Helper()
	agentID, issueID := createClaimReclaimAgentAndIssue(t, context.Background(), runtimeID, label)
	return seedQueuedIssueTask(t, context.Background(), agentID, runtimeID, issueID)
}

func signedDaemonJWT(t *testing.T, userID string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	raw, err := token.SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatalf("sign daemon JWT: %v", err)
	}
	return raw
}

func createDaemonPAT(t *testing.T, userID string) string {
	t.Helper()
	raw := "mul_daemon_auth_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO personal_access_token (
			user_id, name, token_hash, token_prefix, expires_at
		)
		VALUES ($1, 'daemon auth test', $2, $3, now() + interval '1 day')
	`, userID, auth.HashToken(raw), raw[:12]); err != nil {
		t.Fatalf("create daemon PAT: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE token_hash = $1`, auth.HashToken(raw))
	})
	return raw
}

func createDaemonMDT(t *testing.T, daemonID string) string {
	t.Helper()
	raw := "mdt_daemon_auth_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO daemon_token (token_hash, workspace_id, daemon_id, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 day')
	`, auth.HashToken(raw), testWorkspaceID, daemonID); err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM daemon_token WHERE token_hash = $1`, auth.HashToken(raw))
	})
	return raw
}

func newCloudVerifier(t *testing.T, ownerID, instanceID, recordID string) *auth.CloudPATVerifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"valid":              true,
			"owner_id":           ownerID,
			"instance_id":        instanceID,
			"instance_record_id": recordID,
		})
	}))
	t.Cleanup(srv.Close)
	return auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{FleetBaseURL: srv.URL})
}

func serveDaemonAuthed(
	t *testing.T,
	req *http.Request,
	token string,
	cloud *auth.CloudPATVerifier,
	handler http.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	middleware.DaemonAuth(testHandler.Queries, nil, nil, cloud)(handler).ServeHTTP(w, req)
	return w
}

func claimDaemonAuthTask(
	t *testing.T,
	runtimeID, token string,
	cloud *auth.CloudPATVerifier,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/tasks/claim",
		nil,
	)
	req = withURLParam(req, "runtimeId", runtimeID)
	return serveDaemonAuthed(t, req, token, cloud, testHandler.ClaimTaskByRuntime)
}

func assertTaskStillQueued(t *testing.T, taskID string) {
	t.Helper()
	var status string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT status FROM agent_task_queue WHERE id = $1`,
		taskID,
	).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != "queued" {
		t.Fatalf("task status = %q, want queued", status)
	}
}

func TestDaemonRawClaimPATAndJWTRequireRuntimeOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	for _, kind := range []string{"pat", "jwt"} {
		t.Run(kind, func(t *testing.T) {
			attackerID := createDaemonAuthMember(t, "member")
			victimID := createDaemonAuthMember(t, "member")
			runtimeID := createDaemonAuthRuntime(t, victimID, "member-owner-"+kind, nil)
			taskID := seedDaemonAuthClaim(t, runtimeID, "Member owner "+kind)

			var attackerToken, victimToken string
			if kind == "pat" {
				attackerToken = createDaemonPAT(t, attackerID)
				victimToken = createDaemonPAT(t, victimID)
			} else {
				attackerToken = signedDaemonJWT(t, attackerID)
				victimToken = signedDaemonJWT(t, victimID)
			}

			denied := claimDaemonAuthTask(t, runtimeID, attackerToken, nil)
			if denied.Code != http.StatusNotFound {
				t.Fatalf("foreign owner status = %d, want 404: %s", denied.Code, denied.Body.String())
			}
			assertTaskStillQueued(t, taskID)

			allowed := claimDaemonAuthTask(t, runtimeID, victimToken, nil)
			if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), taskID) {
				t.Fatalf("runtime owner claim status = %d: %s", allowed.Code, allowed.Body.String())
			}
		})
	}
}

func TestDaemonRawClaimMDTRequiresExactDaemonID(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ownerID := createDaemonAuthMember(t, "member")
	const daemonID = "exact-mdt-daemon"
	runtimeID := createDaemonAuthRuntime(t, ownerID, daemonID, nil)
	taskID := seedDaemonAuthClaim(t, runtimeID, "Exact MDT")

	denied := claimDaemonAuthTask(t, runtimeID, createDaemonMDT(t, "other-mdt-daemon"), nil)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("wrong daemon status = %d, want 404: %s", denied.Code, denied.Body.String())
	}
	assertTaskStillQueued(t, taskID)

	allowed := claimDaemonAuthTask(t, runtimeID, createDaemonMDT(t, daemonID), nil)
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), taskID) {
		t.Fatalf("matching daemon claim status = %d: %s", allowed.Code, allowed.Body.String())
	}
}

func TestDaemonRawClaimCloudRequiresStampedInstance(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ownerID := createDaemonAuthMember(t, "member")
	runtimeID := createDaemonAuthRuntime(t, ownerID, "cloud-daemon", map[string]any{
		"cloud_instance_id":        "instance-good",
		"cloud_instance_record_id": "record-good",
	})
	taskID := seedDaemonAuthClaim(t, runtimeID, "Cloud instance")

	denied := claimDaemonAuthTask(
		t,
		runtimeID,
		"mcn_wrong_instance",
		newCloudVerifier(t, ownerID, "instance-wrong", "record-wrong"),
	)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("wrong cloud instance status = %d, want 404: %s", denied.Code, denied.Body.String())
	}
	assertTaskStillQueued(t, taskID)

	allowed := claimDaemonAuthTask(
		t,
		runtimeID,
		"mcn_right_instance",
		newCloudVerifier(t, ownerID, "instance-good", "record-good"),
	)
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), taskID) {
		t.Fatalf("matching cloud instance claim status = %d: %s", allowed.Code, allowed.Body.String())
	}
}

func TestDaemonHeartbeatRequiresRuntimeOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	attackerID := createDaemonAuthMember(t, "member")
	ownerID := createDaemonAuthMember(t, "member")
	runtimeID := createDaemonAuthRuntime(t, ownerID, "heartbeat-owner-daemon", nil)
	body, _ := json.Marshal(map[string]any{"runtime_id": runtimeID})

	deniedRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/daemon/heartbeat",
		bytes.NewReader(body),
	)
	deniedRequest.Header.Set("Content-Type", "application/json")
	denied := serveDaemonAuthed(
		t,
		deniedRequest,
		createDaemonPAT(t, attackerID),
		nil,
		testHandler.DaemonHeartbeat,
	)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("foreign heartbeat status = %d, want 404: %s", denied.Code, denied.Body.String())
	}

	allowedRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/daemon/heartbeat",
		bytes.NewReader(body),
	)
	allowedRequest.Header.Set("Content-Type", "application/json")
	allowed := serveDaemonAuthed(
		t,
		allowedRequest,
		createDaemonPAT(t, ownerID),
		nil,
		testHandler.DaemonHeartbeat,
	)
	if allowed.Code != http.StatusOK {
		t.Fatalf("owner heartbeat status = %d: %s", allowed.Code, allowed.Body.String())
	}
}

func TestDaemonWSHeartbeatRevalidatesRevokedMembershipBeforeSideEffects(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	ownerID := createDaemonAuthMember(t, "member")
	runtimeID := createDaemonAuthRuntime(t, ownerID, "ws-revoked-member-"+uuid.NewString(), nil)
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET status = 'offline', last_seen_at = now() - interval '1 hour'
		WHERE id = $1
	`, runtimeID); err != nil {
		t.Fatalf("seed stale runtime: %v", err)
	}
	var beforeSeen time.Time
	if err := testPool.QueryRow(ctx, `SELECT last_seen_at FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&beforeSeen); err != nil {
		t.Fatalf("read initial last_seen_at: %v", err)
	}
	pending, err := testHandler.UpdateStore.Create(ctx, runtimeID, "v-next", ownerID)
	if err != nil {
		t.Fatalf("seed pending update: %v", err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, ownerID); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}

	ack, err := testHandler.HandleDaemonWSHeartbeat(ctx, daemonws.ClientIdentity{
		UserID:       ownerID,
		AuthPath:     middleware.DaemonAuthPathPAT,
		WorkspaceIDs: []string{testWorkspaceID},
		RuntimeIDs:   []string{runtimeID},
	}, runtimeID, false)
	if err != nil {
		t.Fatalf("heartbeat returned error: %v", err)
	}
	if ack == nil || !ack.RuntimeGone || ack.Status != protocol.HeartbeatStatusRuntimeGone {
		t.Fatalf("ack = %+v, want runtime gone", ack)
	}
	stored, err := testHandler.UpdateStore.Get(ctx, pending.ID)
	if err != nil || stored == nil || stored.Status != UpdatePending {
		t.Fatalf("pending command was consumed: stored=%+v err=%v", stored, err)
	}
	var status string
	var afterSeen time.Time
	if err := testPool.QueryRow(ctx, `SELECT status, last_seen_at FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&status, &afterSeen); err != nil {
		t.Fatalf("read runtime after denied heartbeat: %v", err)
	}
	if status != "offline" || !afterSeen.Equal(beforeSeen) {
		t.Fatalf("denied heartbeat mutated liveness: status=%s before=%s after=%s", status, beforeSeen, afterSeen)
	}
}

func TestDaemonWSHeartbeatRevalidatesCloudStampBeforeSideEffects(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	ownerID := createDaemonAuthMember(t, "member")
	runtimeID := createDaemonAuthRuntime(t, ownerID, "ws-cloud-rebind-"+uuid.NewString(), map[string]any{
		"cloud_instance_id":        "instance-original",
		"cloud_instance_record_id": "record-original",
	})
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET status = 'offline',
		    last_seen_at = now() - interval '1 hour',
		    metadata = '{"cloud_instance_id":"instance-rebound","cloud_instance_record_id":"record-rebound"}'::jsonb
		WHERE id = $1
	`, runtimeID); err != nil {
		t.Fatalf("rebind runtime: %v", err)
	}
	var beforeSeen time.Time
	if err := testPool.QueryRow(ctx, `SELECT last_seen_at FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&beforeSeen); err != nil {
		t.Fatalf("read initial last_seen_at: %v", err)
	}
	pending, err := testHandler.UpdateStore.Create(ctx, runtimeID, "v-next-cloud", ownerID)
	if err != nil {
		t.Fatalf("seed pending update: %v", err)
	}

	ack, err := testHandler.HandleDaemonWSHeartbeat(ctx, daemonws.ClientIdentity{
		UserID:                ownerID,
		AuthPath:              middleware.DaemonAuthPathCloudPAT,
		CloudInstanceID:       "instance-original",
		CloudInstanceRecordID: "record-original",
		WorkspaceIDs:          []string{testWorkspaceID},
		RuntimeIDs:            []string{runtimeID},
	}, runtimeID, false)
	if err != nil {
		t.Fatalf("heartbeat returned error: %v", err)
	}
	if ack == nil || !ack.RuntimeGone {
		t.Fatalf("ack = %+v, want runtime gone", ack)
	}
	stored, err := testHandler.UpdateStore.Get(ctx, pending.ID)
	if err != nil || stored == nil || stored.Status != UpdatePending {
		t.Fatalf("pending command was consumed: stored=%+v err=%v", stored, err)
	}
	var status string
	var afterSeen time.Time
	if err := testPool.QueryRow(ctx, `SELECT status, last_seen_at FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&status, &afterSeen); err != nil {
		t.Fatalf("read runtime after denied heartbeat: %v", err)
	}
	if status != "offline" || !afterSeen.Equal(beforeSeen) {
		t.Fatalf("denied heartbeat mutated liveness: status=%s before=%s after=%s", status, beforeSeen, afterSeen)
	}
}

func TestDaemonBatchClaimFiltersMixedRuntimeOwners(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	callerID := createDaemonAuthMember(t, "member")
	foreignID := createDaemonAuthMember(t, "member")
	const daemonID = "mixed-owner-daemon"
	ownRuntime := createDaemonAuthRuntime(t, callerID, daemonID, nil)
	foreignRuntime := createDaemonAuthRuntime(t, foreignID, daemonID, nil)
	ownTask := seedDaemonAuthClaim(t, ownRuntime, "Batch own")
	foreignTask := seedDaemonAuthClaim(t, foreignRuntime, "Batch foreign")

	body, _ := json.Marshal(map[string]any{
		"daemon_id":   daemonID,
		"runtime_ids": []string{ownRuntime, foreignRuntime},
		"max_tasks":   2,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/tasks/claim", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := serveDaemonAuthed(t, req, signedDaemonJWT(t, callerID), nil, testHandler.ClaimTasksByRuntime)
	if w.Code != http.StatusOK {
		t.Fatalf("batch status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), ownTask) {
		t.Fatalf("batch omitted caller-owned task: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), foreignTask) {
		t.Fatalf("batch returned foreign-owned task: %s", w.Body.String())
	}
	assertTaskStillQueued(t, foreignTask)
}

func TestDaemonRegisterCannotTakeOverAnotherMemberRuntime(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	attackerID := createDaemonAuthMember(t, "member")
	victimID := createDaemonAuthMember(t, "member")
	const daemonID = "member-takeover-daemon"
	runtimeID := createDaemonAuthRuntime(t, victimID, daemonID, nil)

	var provider string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT provider FROM agent_runtime WHERE id = $1`,
		runtimeID,
	).Scan(&provider); err != nil {
		t.Fatalf("read provider: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"device_name":  "attacker",
		"runtimes": []map[string]any{{
			"name": "attacker", "type": provider, "version": "1", "status": "online",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := serveDaemonAuthed(t, req, createDaemonPAT(t, attackerID), nil, testHandler.DaemonRegister)
	if w.Code != http.StatusConflict {
		t.Fatalf("takeover registration status = %d, want 409: %s", w.Code, w.Body.String())
	}

	var storedOwner string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT owner_id FROM agent_runtime WHERE id = $1`,
		runtimeID,
	).Scan(&storedOwner); err != nil {
		t.Fatalf("read runtime owner: %v", err)
	}
	if storedOwner != victimID {
		t.Fatalf("runtime owner changed to %s, want %s", storedOwner, victimID)
	}
}

func TestDaemonRegisterCannotTakeOverAnotherCloudInstance(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ownerID := createDaemonAuthMember(t, "member")
	const daemonID = "cloud-takeover-daemon"
	runtimeID := createDaemonAuthRuntime(t, ownerID, daemonID, map[string]any{
		"cloud_instance_id":        "instance-original",
		"cloud_instance_record_id": "record-original",
	})
	var provider string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT provider FROM agent_runtime WHERE id = $1`,
		runtimeID,
	).Scan(&provider); err != nil {
		t.Fatalf("read provider: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"device_name":  "other cloud instance",
		"runtimes": []map[string]any{{
			"name": "other", "type": provider, "version": "1", "status": "online",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := serveDaemonAuthed(
		t,
		req,
		"mcn_other_cloud_instance",
		newCloudVerifier(t, ownerID, "instance-other", "record-other"),
		testHandler.DaemonRegister,
	)
	if w.Code != http.StatusConflict {
		t.Fatalf("cloud takeover status = %d, want 409: %s", w.Code, w.Body.String())
	}

	var metadata []byte
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT metadata FROM agent_runtime WHERE id = $1`,
		runtimeID,
	).Scan(&metadata); err != nil {
		t.Fatalf("read runtime metadata: %v", err)
	}
	if !strings.Contains(string(metadata), "instance-original") ||
		strings.Contains(string(metadata), "instance-other") {
		t.Fatalf("cloud runtime metadata was replaced: %s", metadata)
	}
}

func TestDaemonRegisterCloudCannotAdoptUnstampedRuntime(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ownerID := createDaemonAuthMember(t, "member")
	const daemonID = "cloud-unstamped-takeover-daemon"
	runtimeID := createDaemonAuthRuntime(t, ownerID, daemonID, nil)
	taskID := seedDaemonAuthClaim(t, runtimeID, "Cloud unstamped takeover")

	var provider string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT provider FROM agent_runtime WHERE id = $1`,
		runtimeID,
	).Scan(&provider); err != nil {
		t.Fatalf("read provider: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"device_name":  "cloud attacker",
		"runtimes": []map[string]any{{
			"name": "cloud attacker", "type": provider, "version": "1", "status": "online",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := serveDaemonAuthed(
		t,
		req,
		"mcn_unstamped_takeover",
		newCloudVerifier(t, ownerID, "instance-attacker", "record-attacker"),
		testHandler.DaemonRegister,
	)
	if w.Code != http.StatusConflict {
		t.Fatalf("cloud adoption status = %d, want 409: %s", w.Code, w.Body.String())
	}
	assertTaskStillQueued(t, taskID)

	var metadata []byte
	var agentRuntimeID, taskRuntimeID string
	if err := testPool.QueryRow(context.Background(), `SELECT metadata FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&metadata); err != nil {
		t.Fatalf("read runtime metadata: %v", err)
	}
	if strings.Contains(string(metadata), "instance-attacker") {
		t.Fatalf("unstamped runtime adopted cloud metadata: %s", metadata)
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT a.runtime_id, q.runtime_id
		FROM agent a
		JOIN agent_task_queue q ON q.agent_id = a.id
		WHERE q.id = $1
	`, taskID).Scan(&agentRuntimeID, &taskRuntimeID); err != nil {
		t.Fatalf("read task references: %v", err)
	}
	if agentRuntimeID != runtimeID || taskRuntimeID != runtimeID {
		t.Fatalf("cloud adoption changed refs: agent=%s task=%s runtime=%s", agentRuntimeID, taskRuntimeID, runtimeID)
	}
}

func TestDaemonRegisterNonCloudCannotOverwriteCloudRuntime(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	for _, kind := range []string{"pat", "jwt", "mdt"} {
		t.Run(kind, func(t *testing.T) {
			ownerID := createDaemonAuthMember(t, "member")
			daemonID := "cloud-preserve-" + kind + "-" + uuid.NewString()
			runtimeID := createDaemonAuthRuntime(t, ownerID, daemonID, map[string]any{
				"cloud_instance_id":        "instance-original",
				"cloud_instance_record_id": "record-original",
			})
			var provider string
			if err := testPool.QueryRow(context.Background(), `SELECT provider FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&provider); err != nil {
				t.Fatalf("read provider: %v", err)
			}
			body, _ := json.Marshal(map[string]any{
				"workspace_id": testWorkspaceID,
				"daemon_id":    daemonID,
				"device_name":  "non-cloud overwrite",
				"runtimes": []map[string]any{{
					"name": "non-cloud", "type": provider, "version": "1", "status": "online",
				}},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			var token string
			switch kind {
			case "pat":
				token = createDaemonPAT(t, ownerID)
			case "jwt":
				token = signedDaemonJWT(t, ownerID)
			default:
				token = createDaemonMDT(t, daemonID)
			}
			w := serveDaemonAuthed(t, req, token, nil, testHandler.DaemonRegister)
			if w.Code != http.StatusConflict {
				t.Fatalf("%s overwrite status = %d, want 409: %s", kind, w.Code, w.Body.String())
			}
			var metadata []byte
			if err := testPool.QueryRow(context.Background(), `SELECT metadata FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&metadata); err != nil {
				t.Fatalf("read metadata: %v", err)
			}
			if !strings.Contains(string(metadata), "instance-original") {
				t.Fatalf("%s stripped cloud identity: %s", kind, metadata)
			}
		})
	}
}

func TestDaemonRegisterCloudFailedProfileKeepsVerifiedStamps(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	ownerID := createDaemonAuthMember(t, "member")
	profileID := insertRuntimeProfileFixture(t, ctx, "Cloud failed profile", "codex", "missing-cloud-codex")
	daemonID := "cloud-failed-profile-" + uuid.NewString()
	body, _ := json.Marshal(map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"device_name":  "cloud node",
		"failed_profiles": []map[string]any{{
			"profile_id": profileID,
			"reason":     "command unavailable",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := serveDaemonAuthed(
		t,
		req,
		"mcn_cloud_failed_profile",
		newCloudVerifier(t, ownerID, "instance-failed", "record-failed"),
		testHandler.DaemonRegister,
	)
	if w.Code != http.StatusOK {
		t.Fatalf("failed-profile registration status = %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE daemon_id = $1`, daemonID)
	})
	var instanceID, recordID string
	if err := testPool.QueryRow(ctx, `
		SELECT metadata->>'cloud_instance_id', metadata->>'cloud_instance_record_id'
		FROM agent_runtime
		WHERE workspace_id = $1 AND daemon_id = $2 AND profile_id = $3
	`, testWorkspaceID, daemonID, profileID).Scan(&instanceID, &recordID); err != nil {
		t.Fatalf("read failed-profile stamps: %v", err)
	}
	if instanceID != "instance-failed" || recordID != "record-failed" {
		t.Fatalf("failed-profile stamps = %q/%q", instanceID, recordID)
	}
}

func TestDaemonRegisterLegacyIDsCannotTakeOverAnotherOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	attackerID := createDaemonAuthMember(t, "member")
	victimID := createDaemonAuthMember(t, "member")
	legacyDaemonID := "legacy-victim-" + uuid.NewString()
	newDaemonID := "new-attacker-" + uuid.NewString()

	var legacyRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, $2, 'victim legacy runtime', 'local', 'claude',
			'offline', 'legacy merge auth fixture', '{}'::jsonb, now(),
			'private', $3)
		RETURNING id
	`, testWorkspaceID, legacyDaemonID, victimID).Scan(&legacyRuntimeID); err != nil {
		t.Fatalf("seed victim legacy runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, legacyRuntimeID)
	})
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, legacyRuntimeID, "Legacy merge victim")
	taskID := seedQueuedIssueTask(t, ctx, agentID, legacyRuntimeID, issueID)

	body, _ := json.Marshal(map[string]any{
		"workspace_id":      testWorkspaceID,
		"daemon_id":         newDaemonID,
		"legacy_daemon_ids": []string{legacyDaemonID},
		"device_name":       "attacker",
		"runtimes": []map[string]any{{
			"name": "attacker", "type": "claude", "version": "1", "status": "online",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := serveDaemonAuthed(t, req, createDaemonPAT(t, attackerID), nil, testHandler.DaemonRegister)
	if w.Code != http.StatusOK {
		t.Fatalf("attacker registration status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var response struct {
		Runtimes []struct {
			ID string `json:"id"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Runtimes) != 1 {
		t.Fatalf("decode attacker registration: err=%v body=%s", err, w.Body.String())
	}
	newRuntimeID := response.Runtimes[0].ID
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, newRuntimeID)
	})

	var oldOwner, agentRuntimeID, taskRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT owner_id FROM agent_runtime WHERE id = $1`, legacyRuntimeID).Scan(&oldOwner); err != nil {
		t.Fatalf("victim legacy runtime was removed: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&agentRuntimeID); err != nil {
		t.Fatalf("read victim agent runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskRuntimeID); err != nil {
		t.Fatalf("read victim task runtime: %v", err)
	}
	if oldOwner != victimID || agentRuntimeID != legacyRuntimeID || taskRuntimeID != legacyRuntimeID {
		t.Fatalf(
			"cross-owner legacy merge changed victim state: owner=%s agent_runtime=%s task_runtime=%s",
			oldOwner,
			agentRuntimeID,
			taskRuntimeID,
		)
	}

	var newOwner string
	var legacyTrace *string
	if err := testPool.QueryRow(
		ctx,
		`SELECT owner_id, legacy_daemon_id FROM agent_runtime WHERE id = $1`,
		newRuntimeID,
	).Scan(&newOwner, &legacyTrace); err != nil {
		t.Fatalf("read attacker runtime: %v", err)
	}
	if newOwner != attackerID || legacyTrace != nil {
		t.Fatalf("attacker runtime owner/trace = %s/%v, want %s/<nil>", newOwner, legacyTrace, attackerID)
	}
}

func TestDaemonRegisterLegacyIDsCannotCrossCloudInstance(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	ownerID := createDaemonAuthMember(t, "member")
	legacyDaemonID := "legacy-cloud-victim-" + uuid.NewString()
	newDaemonID := "new-cloud-attacker-" + uuid.NewString()

	var legacyRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, $2, 'victim cloud legacy runtime', 'local', 'claude',
			'offline', 'legacy merge cloud fixture',
			'{"cloud_instance_id":"instance-victim","cloud_instance_record_id":"record-victim"}'::jsonb,
			now(), 'private', $3)
		RETURNING id
	`, testWorkspaceID, legacyDaemonID, ownerID).Scan(&legacyRuntimeID); err != nil {
		t.Fatalf("seed victim cloud legacy runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, legacyRuntimeID)
	})
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, legacyRuntimeID, "Legacy cloud victim")
	taskID := seedQueuedIssueTask(t, ctx, agentID, legacyRuntimeID, issueID)

	body, _ := json.Marshal(map[string]any{
		"workspace_id":      testWorkspaceID,
		"daemon_id":         newDaemonID,
		"legacy_daemon_ids": []string{legacyDaemonID},
		"device_name":       "other cloud instance",
		"runtimes": []map[string]any{{
			"name": "other cloud", "type": "claude", "version": "1", "status": "online",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := serveDaemonAuthed(
		t,
		req,
		"mcn_legacy_other_cloud_instance",
		newCloudVerifier(t, ownerID, "instance-other", "record-other"),
		testHandler.DaemonRegister,
	)
	if w.Code != http.StatusOK {
		t.Fatalf("other cloud registration status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var response struct {
		Runtimes []struct {
			ID string `json:"id"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Runtimes) != 1 {
		t.Fatalf("decode other cloud registration: err=%v body=%s", err, w.Body.String())
	}
	newRuntimeID := response.Runtimes[0].ID
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, newRuntimeID)
	})

	var agentRuntimeID, taskRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&agentRuntimeID); err != nil {
		t.Fatalf("read victim cloud agent runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskRuntimeID); err != nil {
		t.Fatalf("read victim cloud task runtime: %v", err)
	}
	if agentRuntimeID != legacyRuntimeID || taskRuntimeID != legacyRuntimeID {
		t.Fatalf(
			"cross-instance legacy merge changed victim state: agent_runtime=%s task_runtime=%s",
			agentRuntimeID,
			taskRuntimeID,
		)
	}

	var oldCount int
	var newMetadata []byte
	var legacyTrace *string
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id = $1`, legacyRuntimeID).Scan(&oldCount); err != nil {
		t.Fatalf("count victim cloud runtime: %v", err)
	}
	if err := testPool.QueryRow(
		ctx,
		`SELECT metadata, legacy_daemon_id FROM agent_runtime WHERE id = $1`,
		newRuntimeID,
	).Scan(&newMetadata, &legacyTrace); err != nil {
		t.Fatalf("read other cloud runtime: %v", err)
	}
	if oldCount != 1 ||
		!strings.Contains(string(newMetadata), "instance-other") ||
		strings.Contains(string(newMetadata), "instance-victim") ||
		legacyTrace != nil {
		t.Fatalf(
			"cloud runtime separation failed: old_count=%d metadata=%s trace=%v",
			oldCount,
			newMetadata,
			legacyTrace,
		)
	}
}

func TestDaemonRegisterLegacyIDsCannotCrossDaemonWithMDT(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	ownerID := createDaemonAuthMember(t, "member")
	legacyDaemonID := "legacy-mdt-victim-" + uuid.NewString()
	newDaemonID := "new-mdt-attacker-" + uuid.NewString()

	insertRuntime := func(daemonID, name string) string {
		t.Helper()
		var runtimeID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_runtime (
				workspace_id, daemon_id, name, runtime_mode, provider, status,
				device_info, metadata, last_seen_at, visibility, owner_id
			)
			VALUES ($1, $2, $3, 'local', 'claude', 'offline',
				'legacy merge mdt fixture', '{}'::jsonb, now(), 'private', $4)
			RETURNING id
		`, testWorkspaceID, daemonID, name, ownerID).Scan(&runtimeID); err != nil {
			t.Fatalf("seed %s runtime: %v", name, err)
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
		})
		return runtimeID
	}

	legacyRuntimeID := insertRuntime(legacyDaemonID, "victim MDT runtime")
	newRuntimeID := insertRuntime(newDaemonID, "attacker MDT runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, legacyRuntimeID, "Legacy MDT victim")
	taskID := seedQueuedIssueTask(t, ctx, agentID, legacyRuntimeID, issueID)

	body, _ := json.Marshal(map[string]any{
		"workspace_id":      testWorkspaceID,
		"daemon_id":         newDaemonID,
		"legacy_daemon_ids": []string{legacyDaemonID},
		"device_name":       "attacker MDT",
		"runtimes": []map[string]any{{
			"name": "attacker MDT", "type": "claude", "version": "1", "status": "online",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := serveDaemonAuthed(t, req, createDaemonMDT(t, newDaemonID), nil, testHandler.DaemonRegister)
	if w.Code != http.StatusOK {
		t.Fatalf("MDT registration status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var agentRuntimeID, taskRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&agentRuntimeID); err != nil {
		t.Fatalf("read victim MDT agent runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskRuntimeID); err != nil {
		t.Fatalf("read victim MDT task runtime: %v", err)
	}
	var oldCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id = $1`, legacyRuntimeID).Scan(&oldCount); err != nil {
		t.Fatalf("count victim MDT runtime: %v", err)
	}
	if oldCount != 1 || agentRuntimeID != legacyRuntimeID || taskRuntimeID != legacyRuntimeID {
		t.Fatalf(
			"cross-daemon MDT merge changed victim state: old_count=%d agent_runtime=%s task_runtime=%s target=%s",
			oldCount,
			agentRuntimeID,
			taskRuntimeID,
			newRuntimeID,
		)
	}
}

func TestDaemonRuntimeProfileRawArgsRequirePrivilegedOrExactMachineIdentity(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	const fixedSecret = "sentinel-fixed-profile-secret"
	profileID := insertRuntimeProfileFixture(t, context.Background(), "Raw profile auth", "codex", "raw-profile-auth")
	if _, err := testPool.Exec(
		context.Background(),
		`UPDATE runtime_profile SET fixed_args = $2::jsonb WHERE id = $1`,
		profileID,
		fmt.Sprintf(`["--token",%q]`, fixedSecret),
	); err != nil {
		t.Fatalf("seed profile fixed args: %v", err)
	}

	callerID := createDaemonAuthMember(t, "member")
	foreignID := createDaemonAuthMember(t, "member")
	ownerID := createDaemonAuthMember(t, "owner")
	callerRuntime := createDaemonAuthRuntime(t, callerID, "caller-profile-daemon", nil)
	foreignRuntime := createDaemonAuthRuntime(t, foreignID, "foreign-profile-daemon", nil)
	_ = callerRuntime
	token := signedDaemonJWT(t, callerID)

	request := func(daemonID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodGet,
			"/api/daemon/workspaces/"+testWorkspaceID+"/runtime-profiles",
			nil,
		)
		req.Header.Set("X-Multica-Daemon-ID", daemonID)
		req = withURLParam(req, "workspaceId", testWorkspaceID)
		return serveDaemonAuthed(t, req, token, nil, testHandler.DaemonListRuntimeProfiles)
	}

	denied := request("foreign-profile-daemon")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("foreign daemon profile status = %d, want 403: %s", denied.Code, denied.Body.String())
	}
	if strings.Contains(denied.Body.String(), fixedSecret) {
		t.Fatalf("denied profile response leaked fixed args: %s", denied.Body.String())
	}

	selfRegistered := request("caller-profile-daemon")
	if selfRegistered.Code != http.StatusForbidden {
		t.Fatalf("self-registered member status = %d, want 403: %s", selfRegistered.Code, selfRegistered.Body.String())
	}
	if strings.Contains(selfRegistered.Body.String(), fixedSecret) {
		t.Fatalf("self-registered member leaked fixed args: %s", selfRegistered.Body.String())
	}

	ownerReq := httptest.NewRequest(
		http.MethodGet,
		"/api/daemon/workspaces/"+testWorkspaceID+"/runtime-profiles",
		nil,
	)
	ownerReq = withURLParam(ownerReq, "workspaceId", testWorkspaceID)
	ownerAllowed := serveDaemonAuthed(t, ownerReq, signedDaemonJWT(t, ownerID), nil, testHandler.DaemonListRuntimeProfiles)
	if ownerAllowed.Code != http.StatusOK || !strings.Contains(ownerAllowed.Body.String(), fixedSecret) {
		t.Fatalf("owner profile status = %d: %s", ownerAllowed.Code, ownerAllowed.Body.String())
	}

	cloudRuntime := createDaemonAuthRuntime(t, callerID, "cloud-profile-daemon", map[string]any{
		"cloud_instance_id":        "cloud-profile-instance",
		"cloud_instance_record_id": "cloud-profile-record",
	})
	_ = cloudRuntime
	cloudReq := httptest.NewRequest(
		http.MethodGet,
		"/api/daemon/workspaces/"+testWorkspaceID+"/runtime-profiles",
		nil,
	)
	cloudReq.Header.Set("X-Multica-Daemon-ID", "cloud-profile-daemon")
	cloudReq = withURLParam(cloudReq, "workspaceId", testWorkspaceID)
	cloudAllowed := serveDaemonAuthed(
		t,
		cloudReq,
		"mcn_profile_exact",
		newCloudVerifier(t, callerID, "cloud-profile-instance", "cloud-profile-record"),
		testHandler.DaemonListRuntimeProfiles,
	)
	if cloudAllowed.Code != http.StatusOK || !strings.Contains(cloudAllowed.Body.String(), fixedSecret) {
		t.Fatalf("exact cloud profile status = %d: %s", cloudAllowed.Code, cloudAllowed.Body.String())
	}

	mdtReq := httptest.NewRequest(
		http.MethodGet,
		"/api/daemon/workspaces/"+testWorkspaceID+"/runtime-profiles",
		nil,
	)
	mdtReq = withURLParam(mdtReq, "workspaceId", testWorkspaceID)
	mdtAllowed := serveDaemonAuthed(
		t,
		mdtReq,
		createDaemonMDT(t, "mdt-profile-daemon"),
		nil,
		testHandler.DaemonListRuntimeProfiles,
	)
	if mdtAllowed.Code != http.StatusOK || !strings.Contains(mdtAllowed.Body.String(), fixedSecret) {
		t.Fatalf("exact MDT profile status = %d: %s", mdtAllowed.Code, mdtAllowed.Body.String())
	}

	_ = foreignRuntime
}
