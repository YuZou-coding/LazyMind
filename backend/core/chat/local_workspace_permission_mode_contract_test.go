package chat

import (
	"testing"
	"time"

	"lazymind/core/common/orm"
)

func TestWorkspaceBindingPersistsTaskPermissionModeAndVersion(t *testing.T) {
	requireContractColumns(t, "conversation_workspace_bindings",
		"permission_mode",
		"permission_version",
		"updated_at",
	)
}

func TestWorkspacePermissionModeDefaultsToAskAsNeeded(t *testing.T) {
	database := newPromptTestDB(t)
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	conversation := orm.Conversation{
		ID: "task-default-policy", DisplayName: "Default policy", IsTaskConv: true,
		BaseModel: orm.BaseModel{
			CreateUserID: "user-1", CreateUserName: "user-1", CreatedAt: now, UpdatedAt: now,
		},
	}
	workspace := orm.LocalWorkspace{
		ID: "workspace-default-policy", CreateUserID: "user-1", DisplayName: "Project",
		CanonicalPath: "/workspace", DirectoryIdentity: "identity", Status: "active",
		Version: 1, Source: "local", ReadPolicy: "allow", WritePolicy: "allow",
		AuthorizedAt: now, LastUsedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := database.Create(&workspace).Error; err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := database.Exec(
		"INSERT INTO conversation_workspace_bindings (conversation_id, workspace_id, created_at) VALUES (?, ?, ?)",
		conversation.ID, workspace.ID, now,
	).Error; err != nil {
		t.Fatalf("create legacy-style binding: %v", err)
	}
	var mode string
	if err := database.Raw(
		"SELECT permission_mode FROM conversation_workspace_bindings WHERE conversation_id = ?",
		"task-default-policy",
	).Scan(&mode).Error; err != nil {
		t.Fatalf("read permission mode: %v", err)
	}
	if mode != "ask_as_needed" {
		t.Fatalf("permission_mode=%q, want ask_as_needed", mode)
	}
}

func TestWorkspacePermissionModeRejectsUnknownValueBeforeCreatingTask(t *testing.T) {
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")
	err := validateWorkspaceRequestMode(map[string]any{
		"run_in_background":         true,
		"workspace_id":              "workspace-1",
		"workspace_permission_mode": "approve_everything_forever",
	})
	if err == nil {
		t.Fatal("unknown workspace_permission_mode must be rejected")
	}
}

func TestQuickQuestionCannotSubmitWorkspacePermissionMode(t *testing.T) {
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")
	err := validateWorkspaceRequestMode(map[string]any{
		"run_in_background":         false,
		"workspace_permission_mode": "allow_all",
	})
	if err == nil {
		t.Fatal("permission mode must be rejected outside a bound Work task")
	}
}
