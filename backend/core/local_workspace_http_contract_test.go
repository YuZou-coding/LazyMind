package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"lazymind/core/common/orm"
	corestore "lazymind/core/store"
)

type localWorkspaceEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

const localWorkspaceNotFoundHTTPCode = 2002326

func newLocalWorkspaceHTTPContract(t *testing.T) (*orm.DB, http.Handler) {
	t.Helper()
	database := orm.MigrateAllModelsForTest(t)
	statements := []string{
		`CREATE TABLE IF NOT EXISTS local_workspaces (
			id TEXT PRIMARY KEY,
			create_user_id TEXT NOT NULL,
			display_name TEXT NOT NULL,
			canonical_path TEXT NOT NULL,
			directory_identity TEXT NOT NULL,
			status TEXT NOT NULL,
			version INTEGER NOT NULL DEFAULT 1,
			source TEXT NOT NULL,
			read_policy TEXT NOT NULL,
			write_policy TEXT NOT NULL,
			authorized_at DATETIME NOT NULL,
			last_used_at DATETIME NOT NULL,
			revoked_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS conversation_workspace_bindings (
			conversation_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			permission_mode TEXT NOT NULL DEFAULT 'ask_as_needed',
			permission_version INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00',
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
			FOREIGN KEY (workspace_id) REFERENCES local_workspaces(id)
		)`,
	}
	for _, statement := range statements {
		if err := database.Exec(statement).Error; err != nil {
			t.Fatalf("create local workspace contract fixture: %v", err)
		}
	}
	corestore.Init(database.DB, nil, nil)
	t.Cleanup(func() { corestore.Init(nil, nil, nil) })
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")

	router := mux.NewRouter()
	router.UseEncodedPath()
	registerCoreRoutes(router)
	return database, router
}

func insertWorkspaceContractFixture(
	t *testing.T,
	database *orm.DB,
	id, userID, displayName, path, identity, status string,
	lastUsed time.Time,
) {
	t.Helper()
	if err := database.Exec(`INSERT INTO local_workspaces (
		id, create_user_id, display_name, canonical_path, directory_identity,
		status, version, source, read_policy, write_policy,
		authorized_at, last_used_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, 1, 'local', 'allow', 'ask_before_write', ?, ?, ?, ?)`,
		id, userID, displayName, path, identity, status,
		lastUsed, lastUsed, lastUsed, lastUsed,
	).Error; err != nil {
		t.Fatalf("insert workspace %s: %v", id, err)
	}
}

func localWorkspaceRequest(
	t *testing.T,
	handler http.Handler,
	method, target, userID string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		requestBody = *bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, &requestBody)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Id", userID)
	request.Header.Set("X-User-Name", userID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeLocalWorkspaceData(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope localWorkspaceEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response envelope: %v body=%s", err, response.Body.String())
	}
	if envelope.Code != 0 {
		t.Fatalf("response code=%d body=%s", envelope.Code, response.Body.String())
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode response data: %v body=%s", err, response.Body.String())
	}
}

func TestListLocalWorkspacesIsOwnedFilteredRecentAndBounded(t *testing.T) {
	database, handler := newLocalWorkspaceHTTPContract(t)
	base := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	for index := 0; index < 10; index++ {
		insertWorkspaceContractFixture(
			t, database,
			"workspace-"+string(rune('a'+index)),
			"user-1",
			"Project "+string(rune('A'+index)),
			"/Users/alice/Project-"+string(rune('A'+index)),
			"device:inode:"+string(rune('a'+index)),
			"active",
			base.Add(time.Duration(index)*time.Minute),
		)
	}
	insertWorkspaceContractFixture(t, database, "revoked", "user-1", "Project revoked", "/private/revoked", "old", "revoked", base.Add(20*time.Minute))
	insertWorkspaceContractFixture(t, database, "other-user", "user-2", "Project secret", "/private/secret", "other", "active", base.Add(30*time.Minute))

	response := localWorkspaceRequest(t, handler, http.MethodGet, "/local-workspaces?page_size=8&query=Project", "user-1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", response.Code, response.Body.String())
	}
	var result struct {
		Items []struct {
			WorkspaceID string    `json:"workspace_id"`
			DisplayName string    `json:"display_name"`
			Path        string    `json:"path"`
			Status      string    `json:"status"`
			ReadPolicy  string    `json:"read_policy"`
			WritePolicy string    `json:"write_policy"`
			LastUsedAt  time.Time `json:"last_used_at"`
		} `json:"items"`
	}
	decodeLocalWorkspaceData(t, response, &result)
	if len(result.Items) != 8 {
		t.Fatalf("items=%d, want recent limit 8", len(result.Items))
	}
	for index, item := range result.Items {
		if item.Status != "active" || item.ReadPolicy != "allow" || item.WritePolicy != "ask_before_write" {
			t.Fatalf("unexpected policy/status item: %#v", item)
		}
		if strings.Contains(item.Path, "secret") || strings.Contains(item.Path, "revoked") {
			t.Fatalf("list leaked unavailable or other-user workspace: %#v", item)
		}
		if index > 0 && result.Items[index-1].LastUsedAt.Before(item.LastUsedAt) {
			t.Fatalf("items are not ordered by last_used_at descending: %#v", result.Items)
		}
	}
	if strings.Contains(response.Body.String(), "directory_identity") {
		t.Fatalf("platform directory identity leaked to renderer: %s", response.Body.String())
	}
}

func TestRevokeImmediatelyInvalidatesEveryBoundTaskAndReauthorizationDoesNotRevive(t *testing.T) {
	database, handler := newLocalWorkspaceHTTPContract(t)
	now := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	insertWorkspaceContractFixture(t, database, "workspace-old", "user-1", "Documents", "/Users/alice/Documents", "device:inode:old", "active", now)
	archivedAt := now.Add(time.Minute)
	for _, conversation := range []orm.Conversation{
		{
			ID: "task-active", DisplayName: "Active task", IsTaskConv: true,
			BaseModel: orm.BaseModel{CreateUserID: "user-1", CreateUserName: "user-1", CreatedAt: now, UpdatedAt: now},
		},
		{
			ID: "task-archived", DisplayName: "Archived task", IsTaskConv: true, ArchivedAt: &archivedAt,
			BaseModel: orm.BaseModel{CreateUserID: "user-1", CreateUserName: "user-1", CreatedAt: now, UpdatedAt: now},
		},
	} {
		if err := database.Create(&conversation).Error; err != nil {
			t.Fatalf("create conversation %s: %v", conversation.ID, err)
		}
		if err := database.Exec(
			"INSERT INTO conversation_workspace_bindings (conversation_id, workspace_id, created_at) VALUES (?, ?, ?)",
			conversation.ID, "workspace-old", now,
		).Error; err != nil {
			t.Fatalf("bind conversation %s: %v", conversation.ID, err)
		}
	}

	response := localWorkspaceRequest(t, handler, http.MethodPost, "/local-workspaces/workspace-old:revoke", "user-1", map[string]any{"version": 1})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", response.Code, response.Body.String())
	}
	var revoked struct {
		WorkspaceID       string `json:"workspace_id"`
		Status            string `json:"status"`
		AffectedTaskCount int64  `json:"affected_task_count"`
	}
	decodeLocalWorkspaceData(t, response, &revoked)
	if revoked.WorkspaceID != "workspace-old" || revoked.Status != "revoked" || revoked.AffectedTaskCount != 2 {
		t.Fatalf("unexpected revoke result: %#v", revoked)
	}
	var stored struct {
		Status    string
		Version   int
		RevokedAt *time.Time
	}
	if err := database.Raw("SELECT status, version, revoked_at FROM local_workspaces WHERE id = ?", "workspace-old").Scan(&stored).Error; err != nil {
		t.Fatalf("load revoked workspace: %v", err)
	}
	if stored.Status != "revoked" || stored.Version != 2 || stored.RevokedAt == nil {
		t.Fatalf("revoke was not persisted atomically: %#v", stored)
	}
	var bindingCount int64
	if err := database.Raw("SELECT COUNT(*) FROM conversation_workspace_bindings WHERE workspace_id = ?", "workspace-old").Scan(&bindingCount).Error; err != nil {
		t.Fatalf("count retained bindings: %v", err)
	}
	if bindingCount != 2 {
		t.Fatalf("revocation removed audit bindings: got %d want 2", bindingCount)
	}

	insertWorkspaceContractFixture(t, database, "workspace-new", "user-1", "Documents", "/Users/alice/Documents", "device:inode:new", "active", now.Add(time.Hour))
	for _, conversationID := range []string{"task-active", "task-archived"} {
		bindingResponse := localWorkspaceRequest(t, handler, http.MethodGet, "/conversations/"+conversationID+":workspace", "user-1", nil)
		if bindingResponse.Code != http.StatusOK {
			t.Fatalf("binding status=%d body=%s", bindingResponse.Code, bindingResponse.Body.String())
		}
		var binding struct {
			Status      string `json:"status"`
			WorkspaceID string `json:"workspace_id"`
		}
		decodeLocalWorkspaceData(t, bindingResponse, &binding)
		if binding.Status != "revoked" || binding.WorkspaceID != "workspace-old" {
			t.Fatalf("same-path reauthorization revived old task %s: %#v", conversationID, binding)
		}
	}
}

func TestWorkspaceOwnershipFailuresDoNotLeakPaths(t *testing.T) {
	database, handler := newLocalWorkspaceHTTPContract(t)
	now := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	insertWorkspaceContractFixture(t, database, "workspace-private", "user-1", "Private", "/Users/alice/Private", "private-id", "active", now)

	response := localWorkspaceRequest(t, handler, http.MethodPost, "/local-workspaces/workspace-private:revoke", "user-2", map[string]any{"version": 1})
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want ownership-safe 404", response.Code, response.Body.String())
	}
	var envelope localWorkspaceEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("ownership failure must use the stable Core envelope: %v body=%s", err, response.Body.String())
	}
	if envelope.Code != localWorkspaceNotFoundHTTPCode {
		t.Fatalf("ownership failure code=%d, want %d", envelope.Code, localWorkspaceNotFoundHTTPCode)
	}
	if strings.Contains(response.Body.String(), "/Users/alice/Private") {
		t.Fatalf("ownership failure leaked local path: %s", response.Body.String())
	}
}
