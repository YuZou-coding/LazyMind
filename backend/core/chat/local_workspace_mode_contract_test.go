package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lazymind/core/common/orm"
	"lazymind/core/store"
)

const (
	localWorkspaceModeForbiddenCode = 2002321
	localWorkspaceNotFoundCode      = 2002326
	localWorkspaceRevokedCode       = 2002327
	localWorkspaceBindingLockedCode = 2002328
)

func requireWorkspaceContractError(t *testing.T, recorder *httptest.ResponseRecorder, status, code int) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), status)
	}
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error body: %v body=%s", err, recorder.Body.String())
	}
	if payload.Code != code {
		t.Fatalf("body=%s, want stable code %d", recorder.Body.String(), code)
	}
}

func TestChatModeCannotBindLocalWorkspace(t *testing.T) {
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/core/conversations:chat",
		strings.NewReader(`{
  "input":[{"input_type":"text","text":"hello"}],
  "stream":false,
  "workspace_id":"workspace-1"
}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", "user-1")
	recorder := httptest.NewRecorder()

	ChatConversations(recorder, req)

	requireWorkspaceContractError(t, recorder, http.StatusForbidden, localWorkspaceModeForbiddenCode)
}

func TestCloudRuntimeCannotBindLocalWorkspaceEvenForBackgroundTask(t *testing.T) {
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "disabled")
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/core/conversations:chat",
		strings.NewReader(`{
  "input":[{"input_type":"text","text":"hello"}],
  "stream":false,
  "run_in_background":true,
  "workspace_id":"workspace-1"
}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", "user-1")
	recorder := httptest.NewRecorder()

	ChatConversations(recorder, req)

	requireWorkspaceContractError(t, recorder, http.StatusForbidden, localWorkspaceModeForbiddenCode)
}

func newChatWorkspaceContractDB(t *testing.T) *orm.DB {
	t.Helper()
	database := newToolsTestDB(t)
	for _, statement := range []string{
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
	} {
		if err := database.Exec(statement).Error; err != nil {
			t.Fatalf("create workspace fixture: %v", err)
		}
	}
	store.Init(database.DB, nil, nil)
	t.Cleanup(func() { store.Init(nil, nil, nil) })
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")
	baseURL := startChatToolsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/api/scan/sources":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "total": 0})
		case strings.HasPrefix(request.URL.Path, "/api/authservice/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": []any{}}})
		case request.URL.Path == "/api/chat/stream":
			var upstream map[string]any
			if err := json.NewDecoder(request.Body).Decode(&upstream); err != nil {
				t.Fatalf("decode upstream chat body: %v", err)
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"msg":  "success",
				"data": map[string]any{"text": "done", "sources": []any{}},
			})
			conversation, _ := upstream["conversation"].(map[string]any)
			runID, _ := conversation["run_id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"msg":  "success",
				"data": map[string]any{"runtime_event": completedRunEvent(runID, true)},
			})
		default:
			http.NotFound(w, request)
		}
	}))
	t.Setenv("LAZYMIND_CHAT_SERVICE_URL", baseURL)
	t.Setenv("LAZYMIND_AUTH_SERVICE_URL", baseURL)
	t.Setenv("LAZYMIND_SCAN_CONTROL_PLANE_URL", baseURL)
	return database
}

func seedChatWorkspaceContract(t *testing.T, database *orm.DB, workspaceID, userID, status string) {
	t.Helper()
	now := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	if err := database.Exec(`INSERT INTO local_workspaces (
		id, create_user_id, display_name, canonical_path, directory_identity,
		status, version, source, read_policy, write_policy,
		authorized_at, last_used_at, created_at, updated_at
	) VALUES (?, ?, 'Documents', '/Users/alice/Documents', ?, ?, 1, 'desktop', 'allow', 'ask_before_write', ?, ?, ?, ?)`,
		workspaceID, userID, "identity-"+workspaceID, status, now, now, now, now,
	).Error; err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
}

func chatWorkspaceRequest(conversationID, workspaceID string) *http.Request {
	body := `{
  "input":[{"input_type":"text","text":"organize documents"}],
  "stream":false,
  "run_in_background":true,
  "conversation_id":"` + conversationID + `",
  "workspace_id":"` + workspaceID + `"
}`
	request := httptest.NewRequest(http.MethodPost, "/api/core/conversations:chat", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Id", "user-1")
	request.Header.Set("X-User-Name", "user-1")
	return request
}

func TestExistingWorkTaskCannotSwitchWorkspace(t *testing.T) {
	database := newChatWorkspaceContractDB(t)
	seedChatWorkspaceContract(t, database, "workspace-old", "user-1", "active")
	seedChatWorkspaceContract(t, database, "workspace-new", "user-1", "active")
	now := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	conversation := orm.Conversation{
		ID: "task-existing", DisplayName: "Existing", IsTaskConv: true,
		BaseModel: orm.BaseModel{CreateUserID: "user-1", CreateUserName: "user-1", CreatedAt: now, UpdatedAt: now},
	}
	if err := database.Create(&conversation).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if err := database.Exec(
		"INSERT INTO conversation_workspace_bindings (conversation_id, workspace_id, created_at) VALUES (?, ?, ?)",
		conversation.ID, "workspace-old", now,
	).Error; err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	recorder := httptest.NewRecorder()
	ChatConversations(recorder, chatWorkspaceRequest(conversation.ID, "workspace-new"))

	requireWorkspaceContractError(t, recorder, http.StatusConflict, localWorkspaceBindingLockedCode)
	var workspaceID string
	if err := database.Raw(
		"SELECT workspace_id FROM conversation_workspace_bindings WHERE conversation_id = ?",
		conversation.ID,
	).Scan(&workspaceID).Error; err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if workspaceID != "workspace-old" {
		t.Fatalf("locked binding changed to %q", workspaceID)
	}
}

func TestExistingWorkTaskWithoutWorkspaceCannotBeBackfilled(t *testing.T) {
	database := newChatWorkspaceContractDB(t)
	seedChatWorkspaceContract(t, database, "workspace-new", "user-1", "active")
	now := time.Date(2026, 9, 1, 4, 30, 0, 0, time.UTC)
	conversation := orm.Conversation{
		ID: "task-no-workspace", DisplayName: "No workspace", IsTaskConv: true,
		BaseModel: orm.BaseModel{CreateUserID: "user-1", CreateUserName: "user-1", CreatedAt: now, UpdatedAt: now},
	}
	if err := database.Create(&conversation).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	recorder := httptest.NewRecorder()
	ChatConversations(recorder, chatWorkspaceRequest(conversation.ID, "workspace-new"))

	requireWorkspaceContractError(t, recorder, http.StatusConflict, localWorkspaceBindingLockedCode)
	var count int64
	if err := database.Raw(
		"SELECT COUNT(*) FROM conversation_workspace_bindings WHERE conversation_id = ?",
		conversation.ID,
	).Scan(&count).Error; err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if count != 0 {
		t.Fatalf("existing task was backfilled with %d workspace binding", count)
	}
}

func TestFirstWorkMessageRejectsRevokedWorkspaceWithoutLeavingConversation(t *testing.T) {
	database := newChatWorkspaceContractDB(t)
	seedChatWorkspaceContract(t, database, "workspace-revoked", "user-1", "revoked")

	recorder := httptest.NewRecorder()
	ChatConversations(recorder, chatWorkspaceRequest("task-new", "workspace-revoked"))

	requireWorkspaceContractError(t, recorder, http.StatusConflict, localWorkspaceRevokedCode)
	var count int64
	if err := database.Model(&orm.Conversation{}).Where("id = ?", "task-new").Count(&count).Error; err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if count != 0 {
		t.Fatal("rejected workspace left a partially created conversation")
	}
}

func TestFirstWorkMessageCannotBindAnotherUsersWorkspace(t *testing.T) {
	database := newChatWorkspaceContractDB(t)
	seedChatWorkspaceContract(t, database, "workspace-other", "user-2", "active")

	recorder := httptest.NewRecorder()
	ChatConversations(recorder, chatWorkspaceRequest("task-new", "workspace-other"))

	requireWorkspaceContractError(t, recorder, http.StatusNotFound, localWorkspaceNotFoundCode)
	var count int64
	if err := database.Model(&orm.Conversation{}).Where("id = ?", "task-new").Count(&count).Error; err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if count != 0 {
		t.Fatal("ownership failure left a partially created conversation")
	}
}
