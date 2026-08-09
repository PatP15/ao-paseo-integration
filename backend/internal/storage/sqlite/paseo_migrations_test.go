package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionSchemaAppliesFreshWithoutChangingHarnessConstraint(t *testing.T) {
	db := openMigrationTestDB(t)
	if err := migrate(db); err != nil {
		t.Fatalf("migrate fresh database: %v", err)
	}

	wantTables := []string{
		"execution_hosts", "execution_host_capabilities", "project_host_bindings",
		"session_execution_bindings", "work_items", "work_item_deps",
		"work_item_sessions", "session_briefs", "execution_commands",
		"execution_events", "human_questions", "session_checkpoints", "audit_events",
	}
	for _, table := range wantTables {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("find table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s count = %d, want 1", table, count)
		}
	}

	var sessionsSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&sessionsSQL); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	if strings.Contains(sessionsSQL, "paseo") {
		t.Fatalf("sessions.harness constraint was widened for paseo:\n%s", sessionsSQL)
	}
	if columnExists(t, db, "work_items", "board_"+"column") {
		t.Fatal("work_items unexpectedly stores derived board state")
	}
}

func TestExecutionSchemaAppliesWithFutureVersionSeeded(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 41)
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (950, 1)`); err != nil {
		t.Fatalf("seed future migration version: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with future version: %v", err)
	}
	for _, version := range []int{900, 901, 902, 910, 920, 921, 930, 940} {
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = ? AND is_applied = 1`, version).Scan(&applied); err != nil {
			t.Fatalf("query migration %04d: %v", version, err)
		}
		if applied != 1 {
			t.Errorf("migration %04d applied count = %d, want 1", version, applied)
		}
	}
}

func TestOneActiveImplementerConstraint(t *testing.T) {
	db := openMigrationTestDB(t)
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at)
VALUES ('project-1', '/tmp/project-1', '2026-08-05T00:00:00Z')
`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO work_items (
    id, project_id, title, approval_state, lifecycle_fact, created_by_type,
    created_at, updated_at
) VALUES ('work-1', 'project-1', 'Implement integration', 'approved', 'open', 'human', ?, ?)
`, "2026-08-05T00:00:00Z", "2026-08-05T00:00:00Z"); err != nil {
		t.Fatalf("insert work item: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO work_item_sessions (
    work_item_id, session_id, role, attempt_number, is_active_owner, created_at
) VALUES ('work-1', 'session-1', 'implementer', 1, 1, '2026-08-05T00:00:00Z')
`); err != nil {
		t.Fatalf("claim first implementer: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO work_item_sessions (
    work_item_id, session_id, role, attempt_number, is_active_owner, created_at
) VALUES ('work-1', 'session-2', 'implementer', 2, 1, '2026-08-05T00:01:00Z')
`); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("second active implementer error = %v, want unique constraint failure", err)
	}
	if _, err := db.Exec(`
INSERT INTO work_item_sessions (
    work_item_id, session_id, role, attempt_number, is_active_owner, created_at
) VALUES ('work-1', 'session-3', 'reviewer', 1, 1, '2026-08-05T00:02:00Z')
`); err != nil {
		t.Fatalf("active reviewer should not conflict with implementer: %v", err)
	}
}

func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	defer rows.Close()
	found := rows.Next()
	// A mid-iteration error surfaces here, not from Query. Without this a failed
	// scan reads as "column absent", which would make a schema assertion pass
	// for the wrong reason.
	if err := rows.Err(); err != nil {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	return found
}

// TestExecutionZonesReplaceTheTrustZoneEnum pins 0942. The enum it supersedes
// fused autonomy with isolation, which made every "work" project look like it
// needed its own uid; zones carry autonomy, and isolation moves to the host
// where the operating system actually decides it.
func TestExecutionZonesReplaceTheTrustZoneEnum(t *testing.T) {
	db := openMigrationTestDB(t)
	if err := migrate(db); err != nil {
		t.Fatalf("migrate fresh database: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'execution_zones'`).Scan(&count); err != nil {
		t.Fatalf("find execution_zones: %v", err)
	}
	if count != 1 {
		t.Fatalf("execution_zones count = %d, want 1", count)
	}

	for _, col := range []string{"zone_id", "isolated", "isolation_note"} {
		if !columnExists(t, db, "execution_hosts", col) {
			t.Errorf("execution_hosts is missing %s", col)
		}
	}
	if !columnExists(t, db, "project_host_bindings", "required_zone_id") {
		t.Error("project_host_bindings is missing required_zone_id")
	}

	// The legacy enum column stays, unread. Dropping it would need a table
	// rebuild of something session_execution_bindings references, which is not
	// worth it for a column we can simply stop consulting.
	if !columnExists(t, db, "execution_hosts", "trust_zone") {
		t.Error("trust_zone was dropped; a rebuild of execution_hosts was not intended")
	}

	// Isolation must default to FALSE. A host is not isolated until an operator
	// asserts it, because AO cannot verify the claim: no Paseo CLI surface
	// reports the uid a remote daemon runs as. Defaulting true would let an
	// unreviewed host masquerade as a boundary.
	if _, err := db.Exec(`INSERT INTO execution_zones (id, name, description, created_at, updated_at)
	                      VALUES ('z1', 'research', '', '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatalf("insert zone: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO execution_hosts
	    (id, name, backend_type, transport, trust_zone, enabled, max_concurrent_sessions, zone_id, created_at, updated_at)
	    VALUES ('h1','mac','paseo','local','work',1,1,'z1','2026-01-01','2026-01-01')`); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	var isolated int
	if err := db.QueryRow(`SELECT isolated FROM execution_hosts WHERE id = 'h1'`).Scan(&isolated); err != nil {
		t.Fatalf("read isolated: %v", err)
	}
	if isolated != 0 {
		t.Errorf("isolated defaulted to %d, want 0 — isolation must be asserted, never assumed", isolated)
	}
}
