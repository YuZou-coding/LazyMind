package main

import (
	"net/http"
	"testing"
	"time"

	"lazymind/core/common/orm"
)

func TestExistingWorkTaskPermissionModeCanBeUpdatedWithoutChangingWorkspace(t *testing.T) {
	database, handler := newLocalWorkspaceHTTPContract(t)
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	insertWorkspaceContractFixture(
		t, database, "workspace-policy", "user-1", "Project",
		"/Users/alice/Project", "device:inode:policy", "active", now,
	)
	conversation := orm.Conversation{
		ID: "task-policy", DisplayName: "Policy task", IsTaskConv: true,
		BaseModel: orm.BaseModel{
			CreateUserID: "user-1", CreateUserName: "user-1", CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := database.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := database.Exec(
		"INSERT INTO conversation_workspace_bindings (conversation_id, workspace_id, created_at) VALUES (?, ?, ?)",
		conversation.ID, "workspace-policy", now,
	).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}

	response := localWorkspaceRequest(
		t, handler, http.MethodPut,
		"/conversations/task-policy:workspace-permission",
		"user-1",
		map[string]any{"permission_mode": "always_ask", "version": 1},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want permission update success", response.Code, response.Body.String())
	}
	var result struct {
		WorkspaceID       string `json:"workspace_id"`
		PermissionMode    string `json:"permission_mode"`
		PermissionVersion int64  `json:"permission_version"`
	}
	decodeLocalWorkspaceData(t, response, &result)
	if result.WorkspaceID != "workspace-policy" || result.PermissionMode != "always_ask" || result.PermissionVersion != 2 {
		t.Fatalf("unexpected permission update: %#v", result)
	}
}

func TestWorkspaceListCanIncludeInactiveHistoryForExplicitReauthorization(t *testing.T) {
	database, handler := newLocalWorkspaceHTTPContract(t)
	now := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	insertWorkspaceContractFixture(
		t, database, "workspace-revoked-history", "user-1", "Old Project",
		"/Users/alice/Old Project", "device:inode:old", "revoked", now,
	)

	response := localWorkspaceRequest(
		t, handler, http.MethodGet,
		"/local-workspaces?page_size=8&include_inactive=true",
		"user-1", nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", response.Code, response.Body.String())
	}
	var result struct {
		Items []struct {
			WorkspaceID string `json:"workspace_id"`
			Status      string `json:"status"`
		} `json:"items"`
	}
	decodeLocalWorkspaceData(t, response, &result)
	if len(result.Items) != 1 || result.Items[0].WorkspaceID != "workspace-revoked-history" || result.Items[0].Status != "revoked" {
		t.Fatalf("inactive workspace history missing: %#v", result.Items)
	}
}
