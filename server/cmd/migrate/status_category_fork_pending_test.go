package main

import (
	"context"
	"testing"
	"time"
)

// This is the canonical built-in set in upstream migration 339, before the
// fork adds pending_verification in migration 372.
var upstreamLegacyCanonicalStatuses = []string{
	"backlog",
	"todo",
	"in_progress",
	"in_review",
	"done",
	"blocked",
	"cancelled",
}

func TestForkPendingVerificationLifecycleUpgrade(t *testing.T) {
	f := newFixture(t)
	pool := openTestPoolWithSearchPath(t, f.schema)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	if _, err := pool.Exec(ctx, `CREATE TABLE workspace (id UUID PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE issue (
		id UUID PRIMARY KEY,
		status TEXT NOT NULL CONSTRAINT issue_status_check CHECK (
			status IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'cancelled')
		)
	)`); err != nil {
		t.Fatal(err)
	}
	const workspaceID = "00000000-0000-0000-0000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id) VALUES ($1)`, workspaceID); err != nil {
		t.Fatal(err)
	}

	run := func(versions ...string) {
		t.Helper()
		opts := f.opts()
		opts.Files = realMigrationFiles(t, versions, "up")
		opts.Conditions = conditionsForDirection("up")
		if err := runMigrations(ctx, pool, opts); err != nil {
			t.Fatalf("apply migrations %v: %v", versions, err)
		}
	}
	readPending := func() (id, category, name string, isSystem bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT id::text, category, name, is_system
			FROM issue_status WHERE workspace_id = $1 AND key = 'pending_verification'`, workspaceID).
			Scan(&id, &category, &name, &isSystem); err != nil {
			t.Fatalf("read pending_verification row: %v", err)
		}
		return id, category, name, isSystem
	}

	run(
		"332_issue_status",
		"333_issue_status_pkey_index",
		"334_issue_status_primary_key",
		"335_issue_status_workspace_key_index",
		"336_issue_status_workspace_name_index",
		"337_issue_status_open_check",
		"338_issue_status_validate_format",
		"339_seed_issue_status_catalog",
		"372_issue_status_pending_verification",
	)
	id, category, name, isSystem := readPending()
	if category != "pending_verification" || name != "Pending Verification" || !isSystem {
		t.Fatalf("fork seed = (%s, %s, %q, %t)", id, category, name, isSystem)
	}

	run(
		"373_pending_verification_lifecycle_bridge",
		"469_issue_status_lifecycle_categories",
		"478_issue_status_category_expand",
		"491_issue_status_category_backfill",
		"492_issue_status_category_contract",
		"493_issue_status_category_validate",
		"494_issue_status_category_read_contract",
	)
	bridgedID, category, name, isSystem := readPending()
	if bridgedID != id || category != "started" || name != "Pending Verification" || isSystem {
		t.Fatalf("upstream category bridge = (%s, %s, %q, %t), want retained custom row in started", bridgedID, category, name, isSystem)
	}

	run("573_pending_verification_lifecycle_restore")
	restoredID, category, name, isSystem := readPending()
	if restoredID != id || category != "started" || name != "Pending Verification" || !isSystem {
		t.Fatalf("fork restore = (%s, %s, %q, %t), want retained built-in in started", restoredID, category, name, isSystem)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue_status
		WHERE workspace_id = $1 AND key = 'pending_verification'`, workspaceID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pending_verification row count = %d: %v", count, err)
	}
}
