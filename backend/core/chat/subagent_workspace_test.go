package chat

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"lazymind/core/common/orm"
)

func TestAuthoritativeSubagentParamsReplacesForgedWorkspaceContext(t *testing.T) {
	database := newChatWorkspaceContractDB(t)
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")
	now := time.Now().UTC()
	root := t.TempDir()
	previousValidate := validateSubagentWorkspaceDirectory
	validateSubagentWorkspaceDirectory = func(_ context.Context, _ *gorm.DB, _ orm.LocalWorkspace) error { return nil }
	t.Cleanup(func() { validateSubagentWorkspaceDirectory = previousValidate })
	conversation := orm.Conversation{
		ID: "task-subagent-context", DisplayName: "Task", IsTaskConv: true,
		BaseModel: orm.BaseModel{CreateUserID: "user-1", CreateUserName: "user-1", CreatedAt: now, UpdatedAt: now},
	}
	workspace := orm.LocalWorkspace{
		ID: "workspace-authoritative", CreateUserID: "user-1", DisplayName: "Project",
		CanonicalPath: root, DirectoryIdentity: "identity", Status: "active",
		Version: 4, Source: "local", ReadPolicy: "allow", WritePolicy: "allow",
		AuthorizedAt: now, LastUsedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&workspace).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&orm.ConversationWorkspaceBinding{
		ConversationID: conversation.ID, WorkspaceID: workspace.ID,
		PermissionMode: "always_ask", PermissionVersion: 3,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	params, err := authoritativeSubagentParams(t.Context(), database.DB, conversation.ID, "user-1", map[string]any{
		"count": 2,
		"parent_agentic_config": map[string]any{
			"local_fs_sources": []any{map[string]any{"paths": []string{"/etc"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := params["parent_agentic_config"].(map[string]any)
	sources := parent["local_fs_sources"].([]map[string]any)
	if len(sources) != 1 || sources[0]["workspace_id"] != workspace.ID {
		t.Fatalf("unexpected sources: %#v", sources)
	}
	paths := sources[0]["paths"].([]string)
	if len(paths) != 1 || paths[0] != workspace.CanonicalPath {
		t.Fatalf("forged path survived: %#v", paths)
	}
	if parent["workspace_permission_mode"] != "always_ask" || parent["workspace_permission_version"] != int64(3) {
		t.Fatalf("unexpected permission snapshot: %#v", parent)
	}
	if params["count"] != 2 {
		t.Fatalf("ordinary params were not preserved: %#v", params)
	}
}

func TestAuthoritativeSubagentParamsDropsReservedContextWithoutBinding(t *testing.T) {
	database := newChatWorkspaceContractDB(t)
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")
	params, err := authoritativeSubagentParams(t.Context(), database.DB, "missing", "user-1", map[string]any{
		"parent_agentic_config": map[string]any{"local_fs_sources": []any{"forged"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := params["parent_agentic_config"]; exists {
		t.Fatalf("reserved context survived without authoritative binding: %#v", params)
	}
}
