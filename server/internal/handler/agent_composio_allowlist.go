package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	agentComposioAllowlistActivityRevealed = "agent_composio_allowlist_revealed"
	agentComposioAllowlistActivityUpdated  = "agent_composio_allowlist_updated"
)

// AgentComposioAllowlistResponse is deliberately separate from AgentResponse.
// Generic agent resources never contain raw toolkit slugs; authorized humans
// must opt into this narrow, audited reveal endpoint.
type AgentComposioAllowlistResponse struct {
	AgentID      string   `json:"agent_id"`
	ToolkitSlugs []string `json:"toolkit_slugs"`
}

// UpdateAgentComposioAllowlistRequest carries a closed mutation intent.
// "replace" requires a complete non-empty list; "clear" requires no slugs.
// This prevents a redacted/default response replay from becoming destructive.
type UpdateAgentComposioAllowlistRequest struct {
	Intent       string   `json:"intent"`
	ToolkitSlugs []string `json:"toolkit_slugs"`
}

// AgentComposioAllowlistUpdateResponse confirms the committed state without
// echoing the submitted integration footprint into logs, traces, or caches.
type AgentComposioAllowlistUpdateResponse struct {
	AgentID      string `json:"agent_id"`
	ToolkitCount int    `json:"toolkit_count"`
}

// authorizeAgentComposioAllowlist is defense in depth for the router's
// human-only guard. The agent owner or a workspace owner/admin may reveal or
// mutate raw toolkit slugs, and machine credentials are rejected before target
// loading.
func (h *Handler) authorizeAgentComposioAllowlist(w http.ResponseWriter, r *http.Request) (db.Agent, db.Member, bool) {
	if isMachineActorRequest(r) {
		writeError(w, http.StatusForbidden, "machine actors may not access Composio allowlist management endpoints")
		return db.Agent{}, db.Member{}, false
	}

	agentID := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, agentID)
	if !ok {
		return db.Agent{}, db.Member{}, false
	}
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, _ := h.resolveActor(r, requestUserID(r), workspaceID)
	if actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not access Composio allowlist management endpoints")
		return db.Agent{}, db.Member{}, false
	}
	if !h.composioMCPAppsEnabled(r.Context()) {
		writeError(w, http.StatusNotFound, "Composio MCP apps are not enabled")
		return db.Agent{}, db.Member{}, false
	}

	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "agent not found")
	if !ok {
		return db.Agent{}, db.Member{}, false
	}
	if uuidToString(agent.OwnerID) != requestUserID(r) && !roleAllowed(member.Role, "owner", "admin") {
		writeError(w, http.StatusForbidden, "only the agent owner or a workspace owner/admin can manage its Composio allowlist")
		return db.Agent{}, db.Member{}, false
	}
	return agent, member, true
}

// GetAgentComposioToolkitAllowlist reveals the raw slugs only after a
// successful audit write. Audit details intentionally contain counts, not
// toolkit names.
func (h *Handler) GetAgentComposioToolkitAllowlist(w http.ResponseWriter, r *http.Request) {
	agent, member, ok := h.authorizeAgentComposioAllowlist(w, r)
	if !ok {
		return
	}

	details, _ := json.Marshal(map[string]any{
		"agent_id":      uuidToString(agent.ID),
		"agent_name":    agent.Name,
		"toolkit_count": len(agent.ComposioToolkitAllowlist),
	})
	if _, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		WorkspaceID: agent.WorkspaceID,
		IssueID:     pgtype.UUID{},
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     member.UserID,
		Action:      agentComposioAllowlistActivityRevealed,
		Details:     details,
	}); err != nil {
		slog.Error("agent Composio allowlist reveal audit failed; refusing to serve slugs",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "audit log write failed; refusing to serve Composio allowlist without a recorded reveal")
		return
	}

	slugs := append([]string{}, agent.ComposioToolkitAllowlist...)
	writeJSON(w, http.StatusOK, AgentComposioAllowlistResponse{
		AgentID:      uuidToString(agent.ID),
		ToolkitSlugs: slugs,
	})
}

// UpdateAgentComposioToolkitAllowlist performs a complete replace or clear in
// the same transaction as its value-free audit entry. The response and
// workspace-wide event contain only count/redacted metadata.
func (h *Handler) UpdateAgentComposioToolkitAllowlist(w http.ResponseWriter, r *http.Request) {
	agent, member, ok := h.authorizeAgentComposioAllowlist(w, r)
	if !ok {
		return
	}

	var req UpdateAgentComposioAllowlistRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	for field := range rawFields {
		if field != "intent" && field != "toolkit_slugs" {
			writeError(w, http.StatusBadRequest, "only intent and toolkit_slugs are accepted")
			return
		}
	}

	var replacement []string
	switch req.Intent {
	case hiddenMutationIntentReplace:
		replacement = normaliseComposioToolkitAllowlist(req.ToolkitSlugs)
		if len(replacement) == 0 {
			writeError(w, http.StatusBadRequest, "replace intent requires a non-empty toolkit_slugs list")
			return
		}
	case hiddenMutationIntentClear:
		if len(req.ToolkitSlugs) != 0 {
			writeError(w, http.StatusBadRequest, "clear intent requires toolkit_slugs to be omitted or empty")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "intent must be \"replace\" or \"clear\"")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Error("agent Composio allowlist update: begin tx failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to update Composio allowlist")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	var updated db.Agent
	if req.Intent == hiddenMutationIntentClear {
		updated, err = qtx.ClearAgentComposioToolkitAllowlist(r.Context(), agent.ID)
	} else {
		updated, err = qtx.UpdateAgent(r.Context(), db.UpdateAgentParams{
			ID:                       agent.ID,
			ComposioToolkitAllowlist: replacement,
		})
	}
	if err != nil {
		slog.Warn("update agent Composio allowlist failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to update Composio allowlist")
		return
	}

	details, _ := json.Marshal(map[string]any{
		"agent_id":       uuidToString(agent.ID),
		"agent_name":     agent.Name,
		"intent":         req.Intent,
		"previous_count": len(agent.ComposioToolkitAllowlist),
		"toolkit_count":  len(updated.ComposioToolkitAllowlist),
	})
	if _, err := qtx.CreateActivity(r.Context(), db.CreateActivityParams{
		WorkspaceID: agent.WorkspaceID,
		IssueID:     pgtype.UUID{},
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     member.UserID,
		Action:      agentComposioAllowlistActivityUpdated,
		Details:     details,
	}); err != nil {
		slog.Error("agent Composio allowlist update audit failed; rolling back update",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "audit log write failed; Composio allowlist update rolled back")
		return
	}

	resp := agentToResponse(updated)
	if err := h.attachAgentSkills(r.Context(), &resp, updated.ID); err != nil {
		slog.Warn("load agent skills after Composio allowlist update failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(updated.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("agent Composio allowlist update: tx commit failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to update Composio allowlist")
		return
	}

	workspaceID := uuidToString(updated.WorkspaceID)
	h.publish(protocol.EventAgentStatus, workspaceID, "member", uuidToString(member.UserID), map[string]any{
		"agent": broadcastAgentResponse(resp),
	})
	writeJSON(w, http.StatusOK, AgentComposioAllowlistUpdateResponse{
		AgentID:      uuidToString(updated.ID),
		ToolkitCount: len(updated.ComposioToolkitAllowlist),
	})
}
