package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIterationTwoWorkspaceWritePolicyMigrationExistsAndIsReversible(t *testing.T) {
	runner := &Runner{dir: filepath.Join("..", "migrations")}
	catalog, err := runner.loadCatalog()
	if err != nil {
		t.Fatalf("load repository migration catalog: %v", err)
	}
	if len(catalog.Modes) < 3 {
		t.Fatalf("migration modes=%d, want v0_3", len(catalog.Modes))
	}

	const firstWorkspaceMigration = uint64(20260901061506)
	var policyMigration *migrationFile
	for i := range catalog.Modes[2].Dev {
		candidate := &catalog.Modes[2].Dev[i]
		if candidate.FileVersion > firstWorkspaceMigration {
			up, readErr := os.ReadFile(candidate.UpPath)
			if readErr != nil {
				t.Fatalf("read candidate migration %s: %v", candidate.UpPath, readErr)
			}
			if strings.Contains(string(up), "local_workspaces") &&
				strings.Contains(string(up), "write_policy") &&
				strings.Contains(string(up), "allow") {
				policyMigration = candidate
				break
			}
		}
	}
	if policyMigration == nil {
		t.Fatal("missing a new v0_3 migration that changes local workspace write_policy to allow")
	}

	down, err := os.ReadFile(policyMigration.DownPath)
	if err != nil {
		t.Fatalf("read policy rollback migration: %v", err)
	}
	if !strings.Contains(string(down), "ask_before_write") {
		t.Fatalf("rollback %s does not restore ask_before_write", policyMigration.DownPath)
	}
}

func TestIterationTwoWorkspaceAggregateDefaultsToAllow(t *testing.T) {
	aggregate := filepath.Join(
		"..", "migrations", "version_mode", "v0_3",
		"20260805000000_workflow_runtime_release.up.sql",
	)
	body, err := os.ReadFile(aggregate)
	if err != nil {
		t.Fatalf("read v0_3 aggregate migration: %v", err)
	}
	sql := string(body)
	if strings.Contains(sql, "write_policy VARCHAR(32) NOT NULL DEFAULT 'ask_before_write'") ||
		strings.Contains(sql, "write_policy TEXT NOT NULL DEFAULT 'ask_before_write'") {
		t.Fatal("v0_3 aggregate still defaults local workspace write_policy to ask_before_write")
	}
	if !strings.Contains(sql, "write_policy VARCHAR(32) NOT NULL DEFAULT 'allow'") ||
		!strings.Contains(sql, "write_policy TEXT NOT NULL DEFAULT 'allow'") {
		t.Fatal("v0_3 aggregate must default local workspace write_policy to allow for PostgreSQL and SQLite")
	}
}
