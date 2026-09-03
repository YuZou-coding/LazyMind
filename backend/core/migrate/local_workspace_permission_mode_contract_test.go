package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacePermissionModeMigrationIsReversibleAndDefaultsExistingBindings(t *testing.T) {
	runner := &Runner{dir: filepath.Join("..", "migrations")}
	catalog, err := runner.loadCatalog()
	if err != nil {
		t.Fatalf("load repository migration catalog: %v", err)
	}
	const writePolicyMigration = uint64(20260902120000)
	for index := range catalog.Modes[2].Dev {
		candidate := &catalog.Modes[2].Dev[index]
		if candidate.FileVersion <= writePolicyMigration {
			continue
		}
		up, readErr := os.ReadFile(candidate.UpPath)
		if readErr != nil {
			t.Fatalf("read migration %s: %v", candidate.UpPath, readErr)
		}
		sql := string(up)
		if !strings.Contains(sql, "conversation_workspace_bindings") || !strings.Contains(sql, "permission_mode") {
			continue
		}
		if !strings.Contains(sql, "ask_as_needed") || !strings.Contains(sql, "always_ask") || !strings.Contains(sql, "allow_all") {
			t.Fatalf("migration %s does not enforce all permission modes and default", candidate.UpPath)
		}
		down, readErr := os.ReadFile(candidate.DownPath)
		if readErr != nil {
			t.Fatalf("read rollback %s: %v", candidate.DownPath, readErr)
		}
		if !strings.Contains(string(down), "permission_mode") {
			t.Fatalf("rollback %s does not remove permission mode safely", candidate.DownPath)
		}
		return
	}
	t.Fatal("missing a new v0_3 migration for task workspace permission modes")
}
