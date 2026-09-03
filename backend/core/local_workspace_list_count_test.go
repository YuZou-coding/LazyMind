package main

import (
	"net/http"
	"testing"
	"time"

	"lazymind/core/common/orm"
)

func TestListLocalWorkspacesIncludesAffectedTaskCount(t *testing.T) {
	database, handler := newLocalWorkspaceHTTPContract(t)
	now := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	insertWorkspaceContractFixture(
		t, database, "workspace-count", "user-1", "Documents",
		"/Users/alice/Documents", "fsid:darwin:1:2", "active", now,
	)
	conversation := orm.Conversation{
		ID: "task-count", DisplayName: "Bound task", IsTaskConv: true,
		BaseModel: orm.BaseModel{
			CreateUserID: "user-1", CreateUserName: "user-1",
			CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := database.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(
		"INSERT INTO conversation_workspace_bindings (conversation_id, workspace_id, created_at) VALUES (?, ?, ?)",
		conversation.ID, "workspace-count", now,
	).Error; err != nil {
		t.Fatal(err)
	}

	response := localWorkspaceRequest(t, handler, http.MethodGet, "/local-workspaces?page_size=8", "user-1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Items []struct {
			WorkspaceID       string `json:"workspace_id"`
			AffectedTaskCount int64  `json:"affected_task_count"`
		} `json:"items"`
	}
	decodeLocalWorkspaceData(t, response, &result)
	if len(result.Items) != 1 || result.Items[0].AffectedTaskCount != 1 {
		t.Fatalf("unexpected workspace task counts: %#v", result.Items)
	}
}
