package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/migrations"
)

// TestProvisioningForwardMigrationRetainsAudit applies the real migration
// runner to a scratch database on the isolated Postgres (loopback port 5433)
// and checks that provisioning tables have no foreign keys, that an audit row
// survives grant deletion, and that migrating the provisioning files back down
// leaves the audit table in place.
func TestProvisioningForwardMigrationRetainsAudit(t *testing.T) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL is required for the isolated provisioning migration test")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		t.Fatalf("refusing non-loopback DATABASE_URL host %q", host)
	}
	if parsed.Port() != "5433" {
		t.Skipf("isolated provisioning migration test requires host port 5433, got %q", parsed.Port())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	admin := *parsed
	admin.Path = "/postgres"
	adminPool, err := pgxpool.New(ctx, admin.String())
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	dbName := "vis18345_forward_" + hex.EncodeToString(suffix[:])
	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, dbName)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(c, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, dbName))
	}()

	scratch := *parsed
	scratch.Path = "/" + dbName
	pool, err := pgxpool.New(ctx, scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	upFiles, err := migrations.Files("up")
	if err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, pool, runOptions{
		Direction:       "up",
		Files:           upFiles,
		AdvisoryLockKey: 1834501,
		Hooks:           preMigrationHooks,
	}); err != nil {
		t.Fatal(err)
	}

	var fkCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_constraint c
		JOIN pg_class rel ON rel.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = rel.relnamespace
		WHERE c.contype = 'f'
		  AND n.nspname = 'public'
		  AND rel.relname LIKE 'agent_provisioning_%'
	`).Scan(&fkCount); err != nil {
		t.Fatal(err)
	}
	if fkCount != 0 {
		t.Fatalf("agent_provisioning tables have %d foreign keys", fkCount)
	}

	var grantID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_provisioning_grant (
			workspace_id, agent_id, granted_by, max_new_agents, max_concurrent_tasks, invocation_policy
		) VALUES (gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 0, 1, 'private')
		RETURNING id::text
	`).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	var auditID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_provisioning_audit (
			workspace_id, grant_id, actor_type, action, outcome
		) VALUES (gen_random_uuid(), $1, 'member', 'create_grant', 'success')
		RETURNING id::text
	`, grantID).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM agent_provisioning_grant WHERE id = $1`, grantID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_provisioning_audit WHERE id = $1 AND grant_id = $2
	`, auditID, grantID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("deleting a grant removed the audit row for that grant")
	}

	downFiles, err := migrations.Files("down")
	if err != nil {
		t.Fatal(err)
	}
	var provisioningDown []string
	for _, file := range downFiles {
		base := file
		if i := strings.LastIndex(file, "/"); i >= 0 {
			base = file[i+1:]
		}
		if strings.HasPrefix(base, "244_") || strings.HasPrefix(base, "245_") ||
			strings.HasPrefix(base, "246_") || strings.HasPrefix(base, "247_") ||
			strings.HasPrefix(base, "248_") || strings.HasPrefix(base, "249_") ||
			strings.HasPrefix(base, "250_") || strings.HasPrefix(base, "251_") ||
			strings.HasPrefix(base, "252_") || strings.HasPrefix(base, "253_") ||
			strings.HasPrefix(base, "254_") || strings.HasPrefix(base, "255_") {
			provisioningDown = append(provisioningDown, file)
		}
	}
	if len(provisioningDown) != 12 {
		t.Fatalf("provisioning down files = %d, want 12", len(provisioningDown))
	}
	if err := runMigrations(ctx, pool, runOptions{
		Direction:       "down",
		Files:           provisioningDown,
		AdvisoryLockKey: 1834502,
	}); err != nil {
		t.Fatal(err)
	}
	var tableName string
	if err := pool.QueryRow(ctx, `
		SELECT to_regclass('public.agent_provisioning_audit')::text
	`).Scan(&tableName); err != nil {
		t.Fatal(err)
	}
	if tableName == "" {
		t.Fatal("migrate down dropped agent_provisioning_audit")
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_provisioning_audit WHERE id = $1 AND grant_id = $2
	`, auditID, grantID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("migrate down removed the audit row for the deleted grant")
	}
}
