package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type agentStatusDB struct {
	agent db.Agent
}

func (f *agentStatusDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec")
}

func (f *agentStatusDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query")
}

func (f *agentStatusDB) QueryRow(_ context.Context, query string, args ...interface{}) pgx.Row {
	agent := f.agent
	if strings.Contains(query, "UPDATE agent SET status") && len(args) > 1 {
		agent.Status, _ = args[1].(string)
	}
	return agentStatusRow{agent: agent}
}

type agentStatusRow struct {
	agent db.Agent
}

func (r agentStatusRow) Scan(dest ...interface{}) error {
	a := r.agent
	values := []interface{}{
		a.ID, a.WorkspaceID, a.Name, a.AvatarUrl, a.RuntimeMode,
		a.RuntimeConfig, a.Visibility, a.Status, a.MaxConcurrentTasks,
		a.OwnerID, a.CreatedAt, a.UpdatedAt, a.Description, a.RuntimeID,
		a.Instructions, a.ArchivedAt, a.ArchivedBy, a.CustomEnv,
		a.CustomArgs, a.McpConfig, a.Model, a.ThinkingLevel,
		a.ComposioToolkitAllowlist, a.PermissionMode, a.Kind, a.SystemKey,
		a.DisabledRuntimeSkills, a.ServiceTier,
	}
	if len(dest) != len(values) {
		return fmt.Errorf("Scan destinations = %d, want %d", len(dest), len(values))
	}
	for i := range dest {
		target := reflect.ValueOf(dest[i])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return fmt.Errorf("destination %d is not a writable pointer", i)
		}
		value := reflect.ValueOf(values[i])
		if !value.IsValid() || !value.Type().AssignableTo(target.Elem().Type()) {
			return fmt.Errorf("destination %d has type %s, value has type %s", i, target.Elem().Type(), value.Type())
		}
		target.Elem().Set(value)
	}
	return nil
}

// Exercise both task-driven status entry points through their generated-query
// scans and the synchronous bus. The returned db.Agent deliberately contains
// sentinels in every secret-bearing launch field so future refactors cannot
// reintroduce the old full-row WebSocket projection.
func TestTaskServiceAgentStatusPathsPublishMinimalSecretFreeEvents(t *testing.T) {
	const secret = "sentinel-task-service-agent-status-secret"
	bus := events.New()
	persisted := db.Agent{
		ID:            util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		WorkspaceID:   util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		RuntimeID:     util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		Name:          secret,
		Description:   secret,
		Instructions:  secret,
		CustomEnv:     []byte(`{"TOKEN":"` + secret + `"}`),
		CustomArgs:    []byte(`["--token","` + secret + `"]`),
		McpConfig:     []byte(`{"mcpServers":{"private":{"command":"` + secret + `"}}}`),
		RuntimeConfig: []byte(`{"provider":{"api_key":"` + secret + `"}}`),
		Status:        "working",
	}
	service := &TaskService{
		Bus:     bus,
		Queries: db.New(&agentStatusDB{agent: persisted}),
	}

	var captured []events.Event
	bus.Subscribe(protocol.EventAgentStatus, func(event events.Event) {
		captured = append(captured, event)
	})

	service.ReconcileAgentStatus(context.Background(), persisted.ID)
	service.updateAgentStatus(context.Background(), persisted.ID, "idle")

	if len(captured) != 2 {
		t.Fatalf("captured events = %d, want reconcile and direct update", len(captured))
	}
	for index, event := range captured {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal captured event %d: %v", index, err)
		}
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("task-driven agent:status leaked persisted data: %s", encoded)
		}
		if event.Type != protocol.EventAgentStatus {
			t.Fatalf("event %d type = %q", index, event.Type)
		}

		payload, ok := event.Payload.(map[string]any)
		if !ok {
			t.Fatalf("event %d payload type = %T", index, event.Payload)
		}
		agent, ok := payload["agent"].(map[string]any)
		if !ok {
			t.Fatalf("event %d agent payload type = %T", index, payload["agent"])
		}
		wantKeys := []string{"id", "status", "updated_at", "workspace_id"}
		gotKeys := make([]string, 0, len(agent))
		for key := range agent {
			gotKeys = append(gotKeys, key)
		}
		gotSet := make(map[string]bool, len(gotKeys))
		for _, key := range gotKeys {
			gotSet[key] = true
		}
		wantSet := make(map[string]bool, len(wantKeys))
		for _, key := range wantKeys {
			wantSet[key] = true
		}
		if !reflect.DeepEqual(gotSet, wantSet) {
			t.Fatalf("event %d agent status keys = %v, want only %v", index, gotKeys, wantKeys)
		}
	}
	if status := captured[0].Payload.(map[string]any)["agent"].(map[string]any)["status"]; status != "working" {
		t.Fatalf("reconciled status = %#v", status)
	}
	if status := captured[1].Payload.(map[string]any)["agent"].(map[string]any)["status"]; status != "idle" {
		t.Fatalf("updated status = %#v", status)
	}
}
