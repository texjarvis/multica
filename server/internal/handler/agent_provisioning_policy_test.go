package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestProvisioningFieldPolicy(t *testing.T) {
	raw := map[string]json.RawMessage{
		"name":       json.RawMessage(`"n"`),
		"custom_env": json.RawMessage(`{"TOKEN":"secret"}`),
		"runtime_id": json.RawMessage(`"rt"`),
	}
	if got := firstDisallowedField(raw, provisioningCreateFields); got != "custom_env" {
		t.Fatalf("forbidden field = %q", got)
	}
	if got := firstDisallowedField(map[string]json.RawMessage{
		"name": json.RawMessage(`"n"`),
	}, provisioningUpdateFields); got != "" {
		t.Fatalf("allowed field rejected: %q", got)
	}
	if !provisioningMetadataOnly(map[string]json.RawMessage{
		"name":        json.RawMessage(`"n"`),
		"description": json.RawMessage(`"d"`),
		"avatar_url":  json.RawMessage(`null`),
	}) {
		t.Fatal("metadata update was not recognized")
	}
	if provisioningMetadataOnly(map[string]json.RawMessage{"instructions": json.RawMessage(`"x"`)}) {
		t.Fatal("instructions were treated as metadata")
	}
}

func TestInvocationWithinProvisioningPolicy(t *testing.T) {
	private := resolvedPermission{mode: permissionModePrivate}
	if err := invocationWithinPolicy(provisioningPolicyPrivate, private); err != nil {
		t.Fatal(err)
	}
	workspace := resolvedPermission{
		mode:    permissionModePublicTo,
		targets: []targetSpec{{targetType: invocationTargetWorkspace}},
	}
	if err := invocationWithinPolicy(provisioningPolicyPrivate, workspace); err == nil {
		t.Fatal("workspace visibility fit a private grant")
	}
	if err := invocationWithinPolicy(provisioningPolicyWorkspace, workspace); err != nil {
		t.Fatal(err)
	}
	member := resolvedPermission{
		mode:    permissionModePublicTo,
		targets: []targetSpec{{targetType: invocationTargetMember}},
	}
	if err := invocationWithinPolicy(provisioningPolicyWorkspace, member); err != nil {
		t.Fatal(err)
	}
	team := resolvedPermission{
		mode:    permissionModePublicTo,
		targets: []targetSpec{{targetType: invocationTargetTeam}},
	}
	if err := invocationWithinPolicy(provisioningPolicyWorkspace, team); err == nil {
		t.Fatal("team target was allowed")
	}
}

func TestProvisioningModelAndExpiry(t *testing.T) {
	if err := validateProvisioningModel("gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	if err := validateProvisioningModel("bad\nmodel"); err == nil {
		t.Fatal("control character was accepted")
	}
	past := db.AgentProvisioningGrant{
		Status:    "active",
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true},
	}
	if !grantExpired(past) || effectiveProvisioningStatus(past) != "expired" {
		t.Fatal("past expiry was still active")
	}
	open := db.AgentProvisioningGrant{Status: "active"}
	if grantExpired(open) || effectiveProvisioningStatus(open) != "active" {
		t.Fatal("open grant was not active")
	}
	revoked := db.AgentProvisioningGrant{Status: "revoked"}
	if effectiveProvisioningStatus(revoked) != "revoked" {
		t.Fatal("revoked grant was not revoked")
	}
}
