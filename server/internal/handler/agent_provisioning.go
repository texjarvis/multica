package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	provisioningMaxRuntimePairs  = 32
	provisioningMaxSkills        = 64
	provisioningMaxManagedAgents = 32
	provisioningMaxSquads        = 16
	provisioningMaxOriginators   = 32
	provisioningMaxModelRunes    = 200
	provisioningSourceAllowlist  = "allowlist"
	provisioningSourceCreated    = "created"
	provisioningPolicyPrivate    = "private"
	provisioningPolicyWorkspace  = "workspace"
)

// provisioningError is a client-facing denial. code is stored in the audit
// reason. message is the response text and may name a model the caller sent;
// the audit reason never does.
type provisioningError struct {
	status  int
	code    string
	message string
}

func (e *provisioningError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func provErr(status int, code, message string) *provisioningError {
	return &provisioningError{status: status, code: code, message: message}
}

func provUnavailable() *provisioningError {
	return provErr(http.StatusServiceUnavailable, "unavailable", "provisioning authorization unavailable")
}

type provisioningAudit struct {
	workspace  pgtype.UUID
	grantID    pgtype.UUID
	actorType  string
	actorID    pgtype.UUID
	taskID     pgtype.UUID
	originator pgtype.UUID
	action     string
	targetType string
	targetID   string
}

func (a provisioningAudit) params(outcome, reason string) db.InsertProvisioningAuditParams {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	return db.InsertProvisioningAuditParams{
		WorkspaceID:      a.workspace,
		GrantID:          a.grantID,
		ActorType:        a.actorType,
		ActorID:          a.actorID,
		TaskID:           a.taskID,
		OriginatorUserID: a.originator,
		Action:           a.action,
		TargetType:       a.targetType,
		TargetID:         a.targetID,
		Outcome:          outcome,
		Reason:           reason,
	}
}

type provisioningWrite struct {
	tx    pgx.Tx
	q     *db.Queries
	audit provisioningAudit
	grant db.AgentProvisioningGrant
	human bool
}

func (p *provisioningWrite) rollback(ctx context.Context) {
	if p == nil || p.tx == nil {
		return
	}
	_ = p.tx.Rollback(ctx)
}

func (p *provisioningWrite) commitSuccess(ctx context.Context, w http.ResponseWriter) bool {
	if _, err := p.q.InsertProvisioningAudit(ctx, p.audit.params("success", "")); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return false
	}
	if err := p.tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return false
	}
	p.tx = nil
	return true
}

var provisioningCreateFields = map[string]struct{}{
	"name": {}, "description": {}, "instructions": {}, "avatar_url": {},
	"runtime_id": {}, "model": {}, "thinking_level": {}, "service_tier": {},
	"visibility": {}, "permission_mode": {}, "invocation_targets": {},
	"max_concurrent_tasks": {}, "skill_ids": {},
}

var provisioningUpdateFields = map[string]struct{}{
	"name": {}, "description": {}, "instructions": {}, "avatar_url": {},
	"runtime_id": {}, "model": {}, "thinking_level": {}, "service_tier": {},
	"visibility": {}, "permission_mode": {}, "invocation_targets": {},
	"max_concurrent_tasks": {},
}

func firstDisallowedField(raw map[string]json.RawMessage, allowed map[string]struct{}) string {
	if len(raw) == 0 {
		return ""
	}
	fields := make([]string, 0, len(raw))
	for field := range raw {
		if _, ok := allowed[field]; !ok {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func provisioningMetadataOnly(raw map[string]json.RawMessage) bool {
	for field := range raw {
		switch field {
		case "name", "description", "avatar_url":
		default:
			return false
		}
	}
	return true
}

func rejectMachineSquadStructure(w http.ResponseWriter, r *http.Request) bool {
	if !isMachineActorRequest(r) {
		return false
	}
	writeError(w, http.StatusForbidden, "machine actors may not change squad structure")
	return true
}

func rejectMachineAgentLifecycle(w http.ResponseWriter, r *http.Request, action string) bool {
	if !isMachineActorRequest(r) {
		return false
	}
	writeError(w, http.StatusForbidden, "machine actors may not "+action)
	return true
}

func rejectMachineSkillControl(w http.ResponseWriter, r *http.Request) bool {
	if !isMachineActorRequest(r) {
		return false
	}
	writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
	return true
}

func (h *Handler) classifyMachineAgentUpdate(w http.ResponseWriter, r *http.Request, rawFields map[string]json.RawMessage) (bool, bool) {
	source := r.Header.Get("X-Actor-Source")
	if source == "" {
		return false, false
	}
	if source != "task_token" {
		return false, rejectMachineAgentUpdate(w, r, rawFields)
	}
	_, actorOK := middleware.TaskActorFromRequest(r)
	if field := firstDisallowedField(rawFields, provisioningUpdateFields); field != "" {
		message := fmt.Sprintf("field %s is forbidden under a provisioning grant", field)
		if !actorOK {
			writeError(w, http.StatusForbidden, message)
			return false, true
		}
		h.denyProvisioningWithoutMutation(w, r, "update_agent", "agent", chi.URLParam(r, "id"), "forbidden_field:"+field, message)
		return false, true
	}
	// A task token without stamped agent, task, and workspace identity cannot
	// enter the grant path. Metadata stays on the legacy allowlist. Any other
	// field is denied here, before runtime value checks can turn it into a 400.
	if !actorOK {
		return false, rejectMachineAgentUpdate(w, r, rawFields)
	}
	if provisioningMetadataOnly(rawFields) {
		exists, err := h.activeGrantExistsForRequest(r)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "provisioning authorization unavailable")
			return false, true
		}
		if !exists {
			return false, rejectMachineAgentUpdate(w, r, rawFields)
		}
	}
	return true, false
}

func (h *Handler) activeGrantExistsForRequest(r *http.Request) (bool, error) {
	actor, ok := middleware.TaskActorFromRequest(r)
	if !ok {
		return false, nil
	}
	ws, err := util.ParseUUID(actor.WorkspaceID)
	if err != nil {
		return false, nil
	}
	agentID, err := util.ParseUUID(actor.AgentID)
	if err != nil {
		return false, nil
	}
	return h.Queries.ActiveProvisioningGrantExists(r.Context(), db.ActiveProvisioningGrantExistsParams{
		WorkspaceID: ws,
		AgentID:     agentID,
	})
}

func (h *Handler) denyProvisioningWithoutMutation(w http.ResponseWriter, r *http.Request, action, targetType, targetID, code, message string) {
	actor, ok := middleware.TaskActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning authorization unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	grant, _ := h.loadProvisioningGrant(r.Context(), q, actor, false)
	originator, _ := h.verifyProvisioningTask(r.Context(), q, actor, grant)
	audit := auditFromActor(actor, grant, originator, action, targetType, targetID)
	h.finishProvisioningAuthError(w, r.Context(), tx, q, audit, provErr(http.StatusForbidden, code, message))
}

func auditFromActor(actor middleware.TaskActor, grant db.AgentProvisioningGrant, originator pgtype.UUID, action, targetType, targetID string) provisioningAudit {
	ws, _ := util.ParseUUID(actor.WorkspaceID)
	agentID, _ := util.ParseUUID(actor.AgentID)
	taskID, _ := util.ParseUUID(actor.TaskID)
	return provisioningAudit{
		workspace:  ws,
		grantID:    grant.ID,
		actorType:  "agent",
		actorID:    agentID,
		taskID:     taskID,
		originator: originator,
		action:     action,
		targetType: targetType,
		targetID:   targetID,
	}
}

func (h *Handler) finishProvisioningAuthError(w http.ResponseWriter, ctx context.Context, tx pgx.Tx, q *db.Queries, audit provisioningAudit, perr *provisioningError) {
	if perr == nil {
		_ = tx.Rollback(ctx)
		writeError(w, http.StatusInternalServerError, "provisioning authorization unavailable")
		return
	}
	if !audit.workspace.Valid {
		_ = tx.Rollback(ctx)
		writeError(w, perr.status, perr.message)
		return
	}
	if _, err := q.InsertProvisioningAudit(ctx, audit.params("denied", perr.code)); err != nil {
		_ = tx.Rollback(ctx)
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return
	}
	writeError(w, perr.status, perr.message)
}

func (h *Handler) loadProvisioningGrant(ctx context.Context, q *db.Queries, actor middleware.TaskActor, share bool) (db.AgentProvisioningGrant, *provisioningError) {
	ws, err := util.ParseUUID(actor.WorkspaceID)
	if err != nil {
		return db.AgentProvisioningGrant{}, provErr(http.StatusForbidden, "no_grant", "no active provisioning grant for this agent")
	}
	agentID, err := util.ParseUUID(actor.AgentID)
	if err != nil {
		return db.AgentProvisioningGrant{}, provErr(http.StatusForbidden, "no_grant", "no active provisioning grant for this agent")
	}
	var grant db.AgentProvisioningGrant
	if share {
		grant, err = q.ShareActiveProvisioningGrant(ctx, db.ShareActiveProvisioningGrantParams{WorkspaceID: ws, AgentID: agentID})
	} else {
		grant, err = q.LockActiveProvisioningGrant(ctx, db.LockActiveProvisioningGrantParams{WorkspaceID: ws, AgentID: agentID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentProvisioningGrant{}, provErr(http.StatusForbidden, "no_grant", "no active provisioning grant for this agent")
	}
	if err != nil {
		return db.AgentProvisioningGrant{}, provUnavailable()
	}
	if grantExpired(grant) {
		return grant, provErr(http.StatusForbidden, "expired", "provisioning grant is expired")
	}
	return grant, nil
}

func grantExpired(grant db.AgentProvisioningGrant) bool {
	return grant.ExpiresAt.Valid && !grant.ExpiresAt.Time.After(time.Now())
}

func (h *Handler) verifyProvisioningTask(ctx context.Context, q *db.Queries, actor middleware.TaskActor, grant db.AgentProvisioningGrant) (pgtype.UUID, *provisioningError) {
	if !grant.ID.Valid {
		return pgtype.UUID{}, provErr(http.StatusForbidden, "no_grant", "no active provisioning grant for this agent")
	}
	taskID, err := util.ParseUUID(actor.TaskID)
	if err != nil {
		return pgtype.UUID{}, provErr(http.StatusForbidden, "task_mismatch", "provisioning task does not match the grant")
	}
	task, err := q.GetAgentTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, provErr(http.StatusForbidden, "task_mismatch", "provisioning task does not match the grant")
	}
	if err != nil {
		return pgtype.UUID{}, provUnavailable()
	}
	if uuidToString(task.AgentID) != actor.AgentID {
		return pgtype.UUID{}, provErr(http.StatusForbidden, "task_mismatch", "provisioning task does not match the grant")
	}
	switch task.Status {
	case "running", "dispatched", "waiting_local_directory":
	default:
		return pgtype.UUID{}, provErr(http.StatusForbidden, "task_mismatch", "provisioning task does not match the grant")
	}
	if !task.OriginatorUserID.Valid {
		return pgtype.UUID{}, provErr(http.StatusForbidden, "originator_missing", "provisioning task has no originating human")
	}
	allowed, err := q.ProvisioningOriginatorAllowed(ctx, db.ProvisioningOriginatorAllowedParams{
		GrantID: grant.ID,
		UserID:  task.OriginatorUserID,
	})
	if err != nil {
		return task.OriginatorUserID, provUnavailable()
	}
	if !allowed {
		return task.OriginatorUserID, provErr(http.StatusForbidden, "originator_denied", "originating human is not allowed by the provisioning grant")
	}
	return task.OriginatorUserID, nil
}

func invocationWithinPolicy(policy string, perm resolvedPermission) *provisioningError {
	denied := provErr(http.StatusForbidden, "invocation", "invocation policy exceeds the provisioning grant")
	if perm.mode == "" || perm.mode == permissionModePrivate {
		return nil
	}
	if policy != provisioningPolicyWorkspace {
		return denied
	}
	for _, target := range perm.targets {
		if target.targetType == invocationTargetTeam {
			return denied
		}
		if target.targetType != invocationTargetWorkspace && target.targetType != invocationTargetMember {
			return denied
		}
	}
	return nil
}

func (h *Handler) authorizeProvisioningCreate(ctx context.Context, q *db.Queries, actor middleware.TaskActor, req CreateAgentRequest, perm resolvedPermission, runtimeID pgtype.UUID, skillIDs []pgtype.UUID) (db.AgentProvisioningGrant, pgtype.UUID, *provisioningError) {
	grant, perr := h.loadProvisioningGrant(ctx, q, actor, false)
	originator := pgtype.UUID{}
	if perr == nil {
		originator, perr = h.verifyProvisioningTask(ctx, q, actor, grant)
	}
	if perr != nil {
		return grant, originator, perr
	}
	if err := h.runtimeModelAllowed(ctx, q, grant.ID, runtimeID, req.Model); err != nil {
		return grant, originator, err
	}
	if err := skillsAllowed(ctx, q, grant.ID, skillIDs); err != nil {
		return grant, originator, err
	}
	if err := invocationWithinPolicy(grant.InvocationPolicy, perm); err != nil {
		return grant, originator, err
	}
	if req.MaxConcurrentTasks > grant.MaxConcurrentTasks {
		return grant, originator, provErr(http.StatusForbidden, "concurrency", "max_concurrent_tasks exceeds the provisioning grant")
	}
	if _, err := q.IncrementProvisioningGrantCreateCount(ctx, grant.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if grant.NewAgentsCreated >= grant.MaxNewAgents {
				return grant, originator, provErr(http.StatusForbidden, "limit", "new agent limit reached")
			}
			return grant, originator, provErr(http.StatusForbidden, "expired", "provisioning grant is expired")
		}
		return grant, originator, provUnavailable()
	}
	return grant, originator, nil
}

func (h *Handler) runtimeModelAllowed(ctx context.Context, q *db.Queries, grantID, runtimeID pgtype.UUID, model string) *provisioningError {
	ok, err := q.ProvisioningRuntimeAllowed(ctx, db.ProvisioningRuntimeAllowedParams{
		GrantID:   grantID,
		RuntimeID: runtimeID,
		Model:     model,
	})
	if err != nil {
		return provUnavailable()
	}
	if ok {
		return nil
	}
	rows, err := q.ListProvisioningGrantRuntimes(ctx, grantID)
	if err != nil {
		return provUnavailable()
	}
	runtimeKnown := false
	want := uuidToString(runtimeID)
	for _, row := range rows {
		if uuidToString(row.RuntimeID) == want {
			runtimeKnown = true
			break
		}
	}
	if !runtimeKnown {
		return provErr(http.StatusForbidden, "runtime_not_allowed", "runtime is not allowed by the provisioning grant")
	}
	return provErr(http.StatusForbidden, "model_not_allowed", fmt.Sprintf("model %q is not allowed by the provisioning grant", model))
}

func skillsAllowed(ctx context.Context, q *db.Queries, grantID pgtype.UUID, skillIDs []pgtype.UUID) *provisioningError {
	for _, skillID := range skillIDs {
		ok, err := q.ProvisioningSkillAllowed(ctx, db.ProvisioningSkillAllowedParams{GrantID: grantID, SkillID: skillID})
		if err != nil {
			return provUnavailable()
		}
		if !ok {
			return provErr(http.StatusForbidden, "skill_denied", "skill is not allowed by the provisioning grant")
		}
	}
	return nil
}

func provisioningConfigurationTouched(raw map[string]json.RawMessage) bool {
	for _, field := range []string{"runtime_id", "model", "thinking_level", "service_tier"} {
		if _, ok := raw[field]; ok {
			return true
		}
	}
	return false
}

func lockProvisioningAgent(ctx context.Context, q *db.Queries, workspaceID string, agentID pgtype.UUID) (db.Agent, *provisioningError) {
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		return db.Agent{}, provErr(http.StatusNotFound, "agent_denied", "agent not found")
	}
	locked, err := q.LockUserAgentForUpdate(ctx, db.LockUserAgentForUpdateParams{
		ID:          agentID,
		WorkspaceID: ws,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Agent{}, provErr(http.StatusNotFound, "agent_denied", "agent not found")
	}
	if err != nil {
		return db.Agent{}, provUnavailable()
	}
	return locked, nil
}

// beginProvisioningAgentUpdate locks the active grant, then the target agent,
// and returns the locked agent. Callers authorize the resulting configuration
// on this same transaction after they compute it from the locked row.
func (h *Handler) beginProvisioningAgentUpdate(w http.ResponseWriter, r *http.Request, existing db.Agent) (*provisioningWrite, db.Agent, bool) {
	actor, ok := middleware.TaskActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusForbidden, "no active provisioning grant for this agent")
		return nil, db.Agent{}, false
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning authorization unavailable")
		return nil, db.Agent{}, false
	}
	q := h.Queries.WithTx(tx)
	grant, perr := h.loadProvisioningGrant(r.Context(), q, actor, false)
	originator := pgtype.UUID{}
	if perr == nil {
		originator, perr = h.verifyProvisioningTask(r.Context(), q, actor, grant)
	}
	audit := auditFromActor(actor, grant, originator, "update_agent", "agent", uuidToString(existing.ID))
	var locked db.Agent
	if perr == nil {
		var lockErr *provisioningError
		locked, lockErr = lockProvisioningAgent(r.Context(), q, uuidToString(existing.WorkspaceID), existing.ID)
		if lockErr != nil {
			perr = lockErr
		}
	}
	if perr == nil && uuidToString(locked.ID) == actor.AgentID {
		perr = provErr(http.StatusForbidden, "self", "cannot modify the provisioning agent")
	}
	if perr == nil {
		if _, err := q.ProvisioningManagedAgentSource(r.Context(), db.ProvisioningManagedAgentSourceParams{
			GrantID: grant.ID,
			AgentID: locked.ID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				perr = provErr(http.StatusForbidden, "agent_denied", "agent is not managed by the provisioning grant")
			} else {
				perr = provUnavailable()
			}
		}
	}
	if perr != nil {
		h.finishProvisioningAuthError(w, r.Context(), tx, q, audit, perr)
		return nil, db.Agent{}, false
	}
	audit.originator = originator
	audit.targetID = uuidToString(locked.ID)
	return &provisioningWrite{tx: tx, q: q, audit: audit, grant: grant}, locked, true
}

// beginHumanConfigurationUpdate locks every active grant that manages the
// agent, ordered by id, then the agent row. The locks stop a concurrent
// delegated update from committing against a stale runtime/model snapshot.
// The grant allowlist is not applied here: a provisioning grant limits
// delegated machine authority and does not reduce the human owner's
// existing authority. An agent with no grant still takes the agent lock so
// a concurrent delegated update cannot interleave.
func (h *Handler) beginHumanConfigurationUpdate(w http.ResponseWriter, r *http.Request, existing db.Agent) (*provisioningWrite, db.Agent, bool) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning authorization unavailable")
		return nil, db.Agent{}, false
	}
	q := h.Queries.WithTx(tx)
	if _, err := q.LockActiveGrantsForManagedAgent(r.Context(), db.LockActiveGrantsForManagedAgentParams{
		AgentID:     existing.ID,
		WorkspaceID: existing.WorkspaceID,
	}); err != nil {
		_ = tx.Rollback(r.Context())
		writeError(w, http.StatusServiceUnavailable, "provisioning authorization unavailable")
		return nil, db.Agent{}, false
	}
	locked, lockErr := lockProvisioningAgent(r.Context(), q, uuidToString(existing.WorkspaceID), existing.ID)
	if lockErr != nil {
		_ = tx.Rollback(r.Context())
		writeError(w, lockErr.status, lockErr.message)
		return nil, db.Agent{}, false
	}
	return &provisioningWrite{tx: tx, q: q, human: true}, locked, true
}

func (h *Handler) authorizeLockedProvisioningUpdate(
	r *http.Request,
	prov *provisioningWrite,
	req UpdateAgentRequest,
	checkRuntime bool,
	runtimeID pgtype.UUID,
	model string,
	replacePermission bool,
	perm resolvedPermission,
) *provisioningError {
	if checkRuntime {
		if perr := h.runtimeModelAllowed(r.Context(), prov.q, prov.grant.ID, runtimeID, model); perr != nil {
			return perr
		}
	}
	if req.MaxConcurrentTasks != nil && *req.MaxConcurrentTasks > prov.grant.MaxConcurrentTasks {
		return provErr(http.StatusForbidden, "concurrency", "max_concurrent_tasks exceeds the provisioning grant")
	}
	if replacePermission {
		return invocationWithinPolicy(prov.grant.InvocationPolicy, perm)
	}
	return nil
}

func (h *Handler) authorizeProvisioningSkills(ctx context.Context, q *db.Queries, r *http.Request, agent db.Agent, skillIDs []pgtype.UUID, action string) (provisioningAudit, *provisioningError) {
	actor, ok := middleware.TaskActorFromRequest(r)
	if !ok {
		ws, _ := util.ParseUUID(h.resolveWorkspaceID(r))
		return provisioningAudit{
			workspace:  ws,
			actorType:  r.Header.Get("X-Actor-Source"),
			action:     action,
			targetType: "agent",
			targetID:   uuidToString(agent.ID),
		}, provErr(http.StatusForbidden, "no_grant", "no active provisioning grant for this agent")
	}
	grant, perr := h.loadProvisioningGrant(ctx, q, actor, false)
	originator := pgtype.UUID{}
	if perr == nil {
		originator, perr = h.verifyProvisioningTask(ctx, q, actor, grant)
	}
	audit := auditFromActor(actor, grant, originator, action, "agent", uuidToString(agent.ID))
	if perr != nil {
		return audit, perr
	}
	locked, lockErr := lockProvisioningAgent(ctx, q, actor.WorkspaceID, agent.ID)
	if lockErr != nil {
		return audit, lockErr
	}
	agent = locked
	if uuidToString(agent.ID) == actor.AgentID {
		return audit, provErr(http.StatusForbidden, "self", "cannot modify the provisioning agent")
	}
	if _, err := q.ProvisioningManagedAgentSource(ctx, db.ProvisioningManagedAgentSourceParams{
		GrantID: grant.ID,
		AgentID: agent.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return audit, provErr(http.StatusForbidden, "agent_denied", "agent is not managed by the provisioning grant")
		}
		return audit, provUnavailable()
	}
	if err := skillsAllowed(ctx, q, grant.ID, skillIDs); err != nil {
		return audit, err
	}
	return audit, nil
}

func (h *Handler) authorizeProvisioningSquadAdd(ctx context.Context, q *db.Queries, r *http.Request, squadID pgtype.UUID, memberType, role string, memberID pgtype.UUID) (provisioningAudit, *provisioningError) {
	actor, ok := middleware.TaskActorFromRequest(r)
	if !ok {
		ws, _ := util.ParseUUID(h.resolveWorkspaceID(r))
		return provisioningAudit{
			workspace:  ws,
			actorType:  r.Header.Get("X-Actor-Source"),
			action:     "add_squad_member",
			targetType: "squad",
			targetID:   uuidToString(squadID),
		}, provErr(http.StatusForbidden, "no_grant", "no active provisioning grant for this agent")
	}
	grant, perr := h.loadProvisioningGrant(ctx, q, actor, false)
	originator := pgtype.UUID{}
	if perr == nil {
		originator, perr = h.verifyProvisioningTask(ctx, q, actor, grant)
	}
	audit := auditFromActor(actor, grant, originator, "add_squad_member", "squad", uuidToString(squadID))
	if perr != nil {
		return audit, perr
	}
	if memberType != "agent" {
		return audit, provErr(http.StatusForbidden, "membership", "provisioning grants cannot add workspace members to a squad")
	}
	if role != "" && role != "member" {
		return audit, provErr(http.StatusForbidden, "role", "provisioning grants may only add squad members with role member")
	}
	locked, lockErr := lockProvisioningAgent(ctx, q, actor.WorkspaceID, memberID)
	if lockErr != nil {
		return audit, lockErr
	}
	memberID = locked.ID
	if uuidToString(memberID) == actor.AgentID {
		return audit, provErr(http.StatusForbidden, "self", "cannot modify the provisioning agent")
	}
	allowed, err := q.ProvisioningSquadAllowed(ctx, db.ProvisioningSquadAllowedParams{GrantID: grant.ID, SquadID: squadID})
	if err != nil {
		return audit, provUnavailable()
	}
	if !allowed {
		return audit, provErr(http.StatusForbidden, "squad_denied", "squad is not allowed by the provisioning grant")
	}
	if _, err := q.ProvisioningManagedAgentSource(ctx, db.ProvisioningManagedAgentSourceParams{
		GrantID: grant.ID,
		AgentID: memberID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return audit, provErr(http.StatusForbidden, "agent_denied", "agent is not managed by the provisioning grant")
		}
		return audit, provUnavailable()
	}
	return audit, nil
}

type provisioningRuntimeView struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	RuntimeMode string `json:"runtime_mode"`
	Status      string `json:"status"`
	Visibility  string `json:"visibility"`
}

func (h *Handler) listRuntimesForProvisioning(w http.ResponseWriter, r *http.Request) {
	actor, ok := middleware.TaskActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusForbidden, "no active provisioning grant for this agent")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning authorization unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	grant, perr := h.loadProvisioningGrant(r.Context(), q, actor, true)
	originator := pgtype.UUID{}
	if perr == nil {
		originator, perr = h.verifyProvisioningTask(r.Context(), q, actor, grant)
	}
	audit := auditFromActor(actor, grant, originator, "read_runtime_catalog", "runtime", "")
	if perr != nil {
		h.finishProvisioningAuthError(w, r.Context(), tx, q, audit, perr)
		return
	}
	member, ok := h.workspaceMember(w, r, actor.WorkspaceID)
	if !ok {
		return
	}
	allowedIDs, err := q.ListProvisioningGrantRuntimeIDs(r.Context(), grant.ID)
	if err != nil {
		h.finishProvisioningAuthError(w, r.Context(), tx, q, audit, provUnavailable())
		return
	}
	allowed := map[string]struct{}{}
	for _, id := range allowedIDs {
		allowed[uuidToString(id)] = struct{}{}
	}
	runtimes, err := q.ListAgentRuntimes(r.Context(), grant.WorkspaceID)
	if err != nil {
		h.finishProvisioningAuthError(w, r.Context(), tx, q, audit, provUnavailable())
		return
	}
	resp := make([]provisioningRuntimeView, 0, len(allowed))
	for _, rt := range runtimes {
		if _, ok := allowed[uuidToString(rt.ID)]; !ok {
			continue
		}
		if !canUseRuntimeForAgent(member, rt) {
			continue
		}
		resp = append(resp, provisioningRuntimeView{
			ID:          uuidToString(rt.ID),
			WorkspaceID: uuidToString(rt.WorkspaceID),
			Name:        rt.Name,
			Provider:    rt.Provider,
			RuntimeMode: rt.RuntimeMode,
			Status:      rt.Status,
			Visibility:  rt.Visibility,
		})
	}
	audit.action = "read_runtime_catalog"
	if _, err := q.InsertProvisioningAudit(r.Context(), audit.params("success", fmt.Sprintf("allowed_runtime_count=%d", len(resp)))); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type provisioningGrantRequest struct {
	AgentID            string  `json:"agent_id"`
	ExpiresAt          *string `json:"expires_at"`
	MaxNewAgents       int32   `json:"max_new_agents"`
	MaxConcurrentTasks int32   `json:"max_concurrent_tasks"`
	InvocationPolicy   string  `json:"invocation_policy"`
	Runtimes           []struct {
		RuntimeID string   `json:"runtime_id"`
		Models    []string `json:"models"`
	} `json:"runtimes"`
	SkillIDs          []string `json:"skill_ids"`
	ManagedAgentIDs   []string `json:"managed_agent_ids"`
	SquadIDs          []string `json:"squad_ids"`
	OriginatorUserIDs []string `json:"originator_user_ids"`
}

type provisioningGrantRuntimeResponse struct {
	RuntimeID string `json:"runtime_id"`
	Model     string `json:"model"`
}

type provisioningManagedAgentResponse struct {
	AgentID string `json:"agent_id"`
	Source  string `json:"source"`
}

type provisioningGrantResponse struct {
	ID                 string                             `json:"id"`
	WorkspaceID        string                             `json:"workspace_id"`
	AgentID            string                             `json:"agent_id"`
	GrantedBy          string                             `json:"granted_by"`
	Status             string                             `json:"status"`
	EffectiveStatus    string                             `json:"effective_status"`
	ExpiresAt          *string                            `json:"expires_at"`
	RevokedAt          *string                            `json:"revoked_at"`
	RevokedBy          *string                            `json:"revoked_by"`
	MaxNewAgents       int32                              `json:"max_new_agents"`
	NewAgentsCreated   int32                              `json:"new_agents_created"`
	MaxConcurrentTasks int32                              `json:"max_concurrent_tasks"`
	InvocationPolicy   string                             `json:"invocation_policy"`
	Runtimes           []provisioningGrantRuntimeResponse `json:"runtimes"`
	SkillIDs           []string                           `json:"skill_ids"`
	ManagedAgents      []provisioningManagedAgentResponse `json:"managed_agents"`
	SquadIDs           []string                           `json:"squad_ids"`
	OriginatorUserIDs  []string                           `json:"originator_user_ids"`
	CreatedAt          string                             `json:"created_at"`
	UpdatedAt          string                             `json:"updated_at"`
}

func (h *Handler) CreateProvisioningGrant(w http.ResponseWriter, r *http.Request) {
	if isMachineActorRequest(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner")
	if !ok {
		return
	}
	var req provisioningGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	prepared, ok := h.prepareProvisioningGrant(w, r, member, wsUUID, req)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start provisioning grant transaction")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	if status, msg := h.recheckProvisioningGrantRefs(r.Context(), q, member, wsUUID, prepared); msg != "" {
		writeError(w, status, msg)
		return
	}
	grant, err := q.CreateProvisioningGrant(r.Context(), prepared.params)
	if err != nil {
		if isProvisioningActiveConflict(err) {
			writeError(w, http.StatusConflict, "an active provisioning grant already exists for this agent; revoke it before creating another")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid provisioning grant")
		return
	}
	for _, runtime := range prepared.runtimes {
		if err := q.InsertProvisioningGrantRuntime(r.Context(), db.InsertProvisioningGrantRuntimeParams{
			GrantID: grant.ID, RuntimeID: runtime.runtimeID, Model: runtime.model,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "invalid provisioning grant runtime")
			return
		}
	}
	for _, skillID := range prepared.skills {
		if err := q.InsertProvisioningGrantSkill(r.Context(), db.InsertProvisioningGrantSkillParams{
			GrantID: grant.ID, SkillID: skillID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "invalid provisioning grant skill")
			return
		}
	}
	for _, agentID := range prepared.managed {
		if err := q.InsertProvisioningManagedAgent(r.Context(), db.InsertProvisioningManagedAgentParams{
			GrantID: grant.ID, AgentID: agentID, Source: provisioningSourceAllowlist,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "invalid provisioning grant managed agent")
			return
		}
	}
	for _, squadID := range prepared.squads {
		if err := q.InsertProvisioningGrantSquad(r.Context(), db.InsertProvisioningGrantSquadParams{
			GrantID: grant.ID, SquadID: squadID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "invalid provisioning grant squad")
			return
		}
	}
	for _, userID := range prepared.originators {
		if err := q.InsertProvisioningGrantOriginator(r.Context(), db.InsertProvisioningGrantOriginatorParams{
			GrantID: grant.ID, UserID: userID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "invalid provisioning grant originator")
			return
		}
	}
	audit := provisioningAudit{
		workspace:  wsUUID,
		grantID:    grant.ID,
		actorType:  "member",
		actorID:    member.UserID,
		action:     "create_grant",
		targetType: "grant",
		targetID:   uuidToString(grant.ID),
	}
	if _, err := q.InsertProvisioningAudit(r.Context(), audit.params("success", "")); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return
	}
	resp, err := loadProvisioningGrantResponse(r.Context(), q, grant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load provisioning grant")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) InspectProvisioningGrant(w http.ResponseWriter, r *http.Request) {
	if isMachineActorRequest(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner"); !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, r.URL.Query().Get("agent_id"), "agent_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	grant, err := h.Queries.GetLatestProvisioningGrantForAgent(r.Context(), db.GetLatestProvisioningGrantForAgentParams{
		WorkspaceID: wsUUID,
		AgentID:     agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "provisioning grant not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load provisioning grant")
		return
	}
	resp, err := loadProvisioningGrantResponse(r.Context(), h.Queries, grant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load provisioning grant")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) RevokeProvisioningGrant(w http.ResponseWriter, r *http.Request) {
	if isMachineActorRequest(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner")
	if !ok {
		return
	}
	grantID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "grant id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start provisioning grant transaction")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	current, err := q.LockProvisioningGrantByID(r.Context(), db.LockProvisioningGrantByIDParams{
		ID: grantID, WorkspaceID: wsUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "provisioning grant not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning authorization unavailable")
		return
	}
	if current.Status != "active" {
		writeError(w, http.StatusConflict, "provisioning grant is not active")
		return
	}
	revoked, err := q.RevokeProvisioningGrant(r.Context(), db.RevokeProvisioningGrantParams{
		ID: grantID, RevokedBy: member.UserID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "provisioning grant is not active")
		return
	}
	audit := provisioningAudit{
		workspace:  wsUUID,
		grantID:    revoked.ID,
		actorType:  "member",
		actorID:    member.UserID,
		action:     "revoke_grant",
		targetType: "grant",
		targetID:   uuidToString(revoked.ID),
	}
	if _, err := q.InsertProvisioningAudit(r.Context(), audit.params("success", "")); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return
	}
	resp, err := loadProvisioningGrantResponse(r.Context(), q, revoked)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load provisioning grant")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "provisioning audit failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type preparedRuntime struct {
	runtimeID pgtype.UUID
	model     string
}

type preparedGrant struct {
	params      db.CreateProvisioningGrantParams
	runtimes    []preparedRuntime
	skills      []pgtype.UUID
	managed     []pgtype.UUID
	squads      []pgtype.UUID
	originators []pgtype.UUID
}

func (h *Handler) prepareProvisioningGrant(w http.ResponseWriter, r *http.Request, member db.Member, wsUUID pgtype.UUID, req provisioningGrantRequest) (preparedGrant, bool) {
	var prepared preparedGrant
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return prepared, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "agent does not belong to this workspace")
		return prepared, false
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "agent is archived")
		return prepared, false
	}
	if req.MaxNewAgents < 0 || req.MaxNewAgents > 100 {
		writeError(w, http.StatusBadRequest, "max_new_agents must be between 0 and 100")
		return prepared, false
	}
	if req.MaxConcurrentTasks < 1 || req.MaxConcurrentTasks > 100 {
		writeError(w, http.StatusBadRequest, "max_concurrent_tasks must be between 1 and 100")
		return prepared, false
	}
	if req.InvocationPolicy != provisioningPolicyPrivate && req.InvocationPolicy != provisioningPolicyWorkspace {
		writeError(w, http.StatusBadRequest, "invocation_policy must be private or workspace")
		return prepared, false
	}
	var expires pgtype.Timestamptz
	if req.ExpiresAt != nil && strings.TrimSpace(*req.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*req.ExpiresAt))
		if err != nil {
			writeError(w, http.StatusBadRequest, "expires_at must be RFC3339")
			return prepared, false
		}
		if !parsed.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "expires_at must be in the future")
			return prepared, false
		}
		expires = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	seenRuntime := map[string]struct{}{}
	for _, runtime := range req.Runtimes {
		runtimeID, ok := parseUUIDOrBadRequest(w, runtime.RuntimeID, "runtime_id")
		if !ok {
			return prepared, false
		}
		rt, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID: runtimeID, WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "runtime does not belong to this workspace")
			return prepared, false
		}
		if !canUseRuntimeForAgent(member, rt) {
			writeError(w, http.StatusForbidden, "this runtime is private; only its owner or a workspace admin can create agents on it")
			return prepared, false
		}
		if len(runtime.Models) == 0 {
			writeError(w, http.StatusBadRequest, "each runtime requires at least one model")
			return prepared, false
		}
		for _, model := range runtime.Models {
			if err := validateProvisioningModel(model); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return prepared, false
			}
			key := uuidToString(runtimeID) + "\x00" + model
			if _, dup := seenRuntime[key]; dup {
				continue
			}
			seenRuntime[key] = struct{}{}
			prepared.runtimes = append(prepared.runtimes, preparedRuntime{runtimeID: runtimeID, model: model})
		}
	}
	if len(prepared.runtimes) == 0 {
		writeError(w, http.StatusBadRequest, "at least one runtime and model is required")
		return prepared, false
	}
	if len(prepared.runtimes) > provisioningMaxRuntimePairs {
		writeError(w, http.StatusBadRequest, "too many runtime and model pairs")
		return prepared, false
	}
	prepared.skills, ok = h.uniqueWorkspaceSkills(w, r, wsUUID, req.SkillIDs)
	if !ok {
		return prepared, false
	}
	prepared.managed, ok = h.uniqueManagedAgents(w, r, wsUUID, agentID, req.ManagedAgentIDs)
	if !ok {
		return prepared, false
	}
	prepared.squads, ok = h.uniqueWorkspaceSquads(w, r, wsUUID, req.SquadIDs)
	if !ok {
		return prepared, false
	}
	prepared.originators, ok = h.uniqueOriginators(w, r, wsUUID, req.OriginatorUserIDs)
	if !ok {
		return prepared, false
	}
	prepared.params = db.CreateProvisioningGrantParams{
		WorkspaceID:        wsUUID,
		AgentID:            agentID,
		GrantedBy:          member.UserID,
		ExpiresAt:          expires,
		MaxNewAgents:       req.MaxNewAgents,
		MaxConcurrentTasks: req.MaxConcurrentTasks,
		InvocationPolicy:   req.InvocationPolicy,
	}
	return prepared, true
}

func (h *Handler) recheckProvisioningGrantRefs(ctx context.Context, q *db.Queries, member db.Member, ws pgtype.UUID, prepared preparedGrant) (int, string) {
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: prepared.params.AgentID, WorkspaceID: ws})
	if err != nil {
		return http.StatusBadRequest, "agent does not belong to this workspace"
	}
	if agent.ArchivedAt.Valid {
		return http.StatusBadRequest, "agent is archived"
	}
	seenRuntime := map[string]struct{}{}
	for _, runtime := range prepared.runtimes {
		key := uuidToString(runtime.runtimeID)
		if _, ok := seenRuntime[key]; ok {
			continue
		}
		seenRuntime[key] = struct{}{}
		rt, err := q.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
			ID: runtime.runtimeID, WorkspaceID: ws,
		})
		if err != nil {
			return http.StatusBadRequest, "runtime does not belong to this workspace"
		}
		if !canUseRuntimeForAgent(member, rt) {
			return http.StatusForbidden, "this runtime is private; only its owner or a workspace admin can create agents on it"
		}
	}
	for _, skillID := range prepared.skills {
		if _, err := q.GetSkillInWorkspace(ctx, db.GetSkillInWorkspaceParams{ID: skillID, WorkspaceID: ws}); err != nil {
			return http.StatusBadRequest, "skill does not belong to this workspace"
		}
	}
	granteeID := uuidToString(prepared.params.AgentID)
	for _, agentID := range prepared.managed {
		if uuidToString(agentID) == granteeID {
			return http.StatusBadRequest, "the provisioning agent cannot be a managed agent of its own grant"
		}
		managed, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: ws})
		if err != nil {
			return http.StatusBadRequest, "managed agent does not belong to this workspace"
		}
		if managed.ArchivedAt.Valid {
			return http.StatusBadRequest, "managed agent is archived"
		}
	}
	for _, squadID := range prepared.squads {
		squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: ws})
		if err != nil {
			return http.StatusBadRequest, "squad does not belong to this workspace"
		}
		if squad.ArchivedAt.Valid {
			return http.StatusBadRequest, "squad is archived"
		}
	}
	for _, userID := range prepared.originators {
		if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID: userID, WorkspaceID: ws,
		}); err != nil {
			return http.StatusBadRequest, "originator is not a member of this workspace"
		}
	}
	return 0, ""
}

func validateProvisioningModel(model string) error {
	if len([]rune(model)) > provisioningMaxModelRunes {
		return fmt.Errorf("model must be %d characters or fewer", provisioningMaxModelRunes)
	}
	for _, r := range model {
		if unicode.IsControl(r) {
			return fmt.Errorf("model contains a control character")
		}
	}
	return nil
}

func (h *Handler) uniqueWorkspaceSkills(w http.ResponseWriter, r *http.Request, wsUUID pgtype.UUID, ids []string) ([]pgtype.UUID, bool) {
	if len(ids) > provisioningMaxSkills {
		writeError(w, http.StatusBadRequest, "too many skills")
		return nil, false
	}
	out := make([]pgtype.UUID, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		skillID, ok := parseUUIDOrBadRequest(w, id, "skill_id")
		if !ok {
			return nil, false
		}
		key := uuidToString(skillID)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if _, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{ID: skillID, WorkspaceID: wsUUID}); err != nil {
			writeError(w, http.StatusBadRequest, "skill does not belong to this workspace")
			return nil, false
		}
		out = append(out, skillID)
	}
	return out, true
}

func (h *Handler) uniqueManagedAgents(w http.ResponseWriter, r *http.Request, wsUUID, grantee pgtype.UUID, ids []string) ([]pgtype.UUID, bool) {
	if len(ids) > provisioningMaxManagedAgents {
		writeError(w, http.StatusBadRequest, "too many managed agents")
		return nil, false
	}
	out := make([]pgtype.UUID, 0, len(ids))
	seen := map[string]struct{}{}
	granteeID := uuidToString(grantee)
	for _, id := range ids {
		agentID, ok := parseUUIDOrBadRequest(w, id, "managed_agent_id")
		if !ok {
			return nil, false
		}
		if uuidToString(agentID) == granteeID {
			writeError(w, http.StatusBadRequest, "the provisioning agent cannot be a managed agent of its own grant")
			return nil, false
		}
		key := uuidToString(agentID)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: wsUUID})
		if err != nil {
			writeError(w, http.StatusBadRequest, "managed agent does not belong to this workspace")
			return nil, false
		}
		if agent.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "managed agent is archived")
			return nil, false
		}
		out = append(out, agentID)
	}
	return out, true
}

func (h *Handler) uniqueWorkspaceSquads(w http.ResponseWriter, r *http.Request, wsUUID pgtype.UUID, ids []string) ([]pgtype.UUID, bool) {
	if len(ids) > provisioningMaxSquads {
		writeError(w, http.StatusBadRequest, "too many squads")
		return nil, false
	}
	out := make([]pgtype.UUID, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		squadID, ok := parseUUIDOrBadRequest(w, id, "squad_id")
		if !ok {
			return nil, false
		}
		key := uuidToString(squadID)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: wsUUID})
		if err != nil {
			writeError(w, http.StatusBadRequest, "squad does not belong to this workspace")
			return nil, false
		}
		if squad.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "squad is archived")
			return nil, false
		}
		out = append(out, squadID)
	}
	return out, true
}

func (h *Handler) uniqueOriginators(w http.ResponseWriter, r *http.Request, wsUUID pgtype.UUID, ids []string) ([]pgtype.UUID, bool) {
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "at least one originator is required")
		return nil, false
	}
	if len(ids) > provisioningMaxOriginators {
		writeError(w, http.StatusBadRequest, "too many originators")
		return nil, false
	}
	out := make([]pgtype.UUID, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		userID, ok := parseUUIDOrBadRequest(w, id, "originator_user_id")
		if !ok {
			return nil, false
		}
		key := uuidToString(userID)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID: userID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "originator is not a member of this workspace")
			return nil, false
		}
		out = append(out, userID)
	}
	if len(out) == 0 {
		writeError(w, http.StatusBadRequest, "at least one originator is required")
		return nil, false
	}
	return out, true
}

func loadProvisioningGrantResponse(ctx context.Context, q *db.Queries, grant db.AgentProvisioningGrant) (provisioningGrantResponse, error) {
	resp := provisioningGrantResponse{
		ID:                 uuidToString(grant.ID),
		WorkspaceID:        uuidToString(grant.WorkspaceID),
		AgentID:            uuidToString(grant.AgentID),
		GrantedBy:          uuidToString(grant.GrantedBy),
		Status:             grant.Status,
		EffectiveStatus:    effectiveProvisioningStatus(grant),
		ExpiresAt:          timestampToPtr(grant.ExpiresAt),
		RevokedAt:          timestampToPtr(grant.RevokedAt),
		RevokedBy:          uuidToPtr(grant.RevokedBy),
		MaxNewAgents:       grant.MaxNewAgents,
		NewAgentsCreated:   grant.NewAgentsCreated,
		MaxConcurrentTasks: grant.MaxConcurrentTasks,
		InvocationPolicy:   grant.InvocationPolicy,
		Runtimes:           []provisioningGrantRuntimeResponse{},
		SkillIDs:           []string{},
		ManagedAgents:      []provisioningManagedAgentResponse{},
		SquadIDs:           []string{},
		OriginatorUserIDs:  []string{},
		CreatedAt:          timestampToString(grant.CreatedAt),
		UpdatedAt:          timestampToString(grant.UpdatedAt),
	}
	runtimes, err := q.ListProvisioningGrantRuntimes(ctx, grant.ID)
	if err != nil {
		return resp, err
	}
	for _, runtime := range runtimes {
		resp.Runtimes = append(resp.Runtimes, provisioningGrantRuntimeResponse{
			RuntimeID: uuidToString(runtime.RuntimeID),
			Model:     runtime.Model,
		})
	}
	skills, err := q.ListProvisioningGrantSkills(ctx, grant.ID)
	if err != nil {
		return resp, err
	}
	for _, skillID := range skills {
		resp.SkillIDs = append(resp.SkillIDs, uuidToString(skillID))
	}
	managed, err := q.ListProvisioningGrantManagedAgents(ctx, grant.ID)
	if err != nil {
		return resp, err
	}
	for _, agent := range managed {
		resp.ManagedAgents = append(resp.ManagedAgents, provisioningManagedAgentResponse{
			AgentID: uuidToString(agent.AgentID),
			Source:  agent.Source,
		})
	}
	squads, err := q.ListProvisioningGrantSquads(ctx, grant.ID)
	if err != nil {
		return resp, err
	}
	for _, squadID := range squads {
		resp.SquadIDs = append(resp.SquadIDs, uuidToString(squadID))
	}
	originators, err := q.ListProvisioningGrantOriginators(ctx, grant.ID)
	if err != nil {
		return resp, err
	}
	for _, userID := range originators {
		resp.OriginatorUserIDs = append(resp.OriginatorUserIDs, uuidToString(userID))
	}
	return resp, nil
}

func effectiveProvisioningStatus(grant db.AgentProvisioningGrant) string {
	if grant.Status == "revoked" {
		return "revoked"
	}
	if grantExpired(grant) {
		return "expired"
	}
	return "active"
}

func isProvisioningActiveConflict(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	return isUniqueViolation(err)
}
