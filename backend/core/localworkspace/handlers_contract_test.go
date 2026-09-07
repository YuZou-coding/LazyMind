package localworkspace

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"lazymind/core/common/orm"
	"lazymind/core/store"
)

type workspaceContractEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

func newWorkspaceHandlerContractDB(t *testing.T) *orm.DB {
	t.Helper()
	database := orm.MigrateAllModelsForTest(t)
	if err := database.AutoMigrate(&orm.LocalWorkspace{}, &orm.ConversationWorkspaceBinding{}); err != nil {
		t.Fatalf("migrate workspace contract fixture: %v", err)
	}
	store.Init(database.DB, nil, nil)
	t.Cleanup(func() { store.Init(nil, nil, nil) })
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN", "workspace-contract-host")
	return database
}

func createWorkspaceContractConversation(t *testing.T, database *orm.DB, id, userID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := database.Create(&orm.Conversation{
		ID: id, DisplayName: id, IsTaskConv: true,
		BaseModel: orm.BaseModel{
			CreateUserID: userID, CreateUserName: userID, CreatedAt: now, UpdatedAt: now,
		},
	}).Error; err != nil {
		t.Fatalf("create conversation %s: %v", id, err)
	}
}

func createWorkspaceContractGrant(t *testing.T, database *orm.DB, id, userID, root, status string, version int64, lastUsed time.Time) orm.LocalWorkspace {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize workspace root: %v", err)
	}
	identity, err := currentDirectoryIdentity(canonical)
	if err != nil {
		t.Fatalf("read workspace identity: %v", err)
	}
	row := orm.LocalWorkspace{
		ID: id, CreateUserID: userID, DisplayName: filepath.Base(canonical),
		CanonicalPath: canonical, DirectoryIdentity: identity, Status: status, Version: version,
		Source: "local", ReadPolicy: ReadPolicyAllow, WritePolicy: WritePolicyAllow,
		AuthorizedAt: lastUsed, LastUsedAt: lastUsed, CreatedAt: lastUsed, UpdatedAt: lastUsed,
	}
	if err := database.Create(&row).Error; err != nil {
		t.Fatalf("create workspace %s: %v", id, err)
	}
	return row
}

func bindWorkspaceContractConversation(t *testing.T, database *orm.DB, conversationID, workspaceID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := database.Create(&orm.ConversationWorkspaceBinding{
		ConversationID: conversationID, WorkspaceID: workspaceID,
		PermissionMode: PermissionAskAsNeeded, PermissionVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("bind conversation %s: %v", conversationID, err)
	}
}

func decodeWorkspaceContractData(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope workspaceContractEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, response.Body.String())
	}
	if envelope.Code != 0 {
		t.Fatalf("application code=%d body=%s", envelope.Code, response.Body.String())
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode data: %v data=%s", err, string(envelope.Data))
	}
}

func TestListSearchesOwnedRecentWorkspacesByNameAndPath(t *testing.T) {
	database := newWorkspaceHandlerContractDB(t)
	now := time.Now().UTC()
	projectRoot := t.TempDir()
	archiveRoot := t.TempDir()
	otherRoot := t.TempDir()
	project := createWorkspaceContractGrant(t, database, "workspace-project", "user-1", projectRoot, StatusActive, 1, now)
	if err := database.Model(&orm.LocalWorkspace{}).Where("id = ?", project.ID).Update("display_name", "Quarterly Notes").Error; err != nil {
		t.Fatal(err)
	}
	createWorkspaceContractGrant(t, database, "workspace-archive", "user-1", archiveRoot, StatusRevoked, 2, now.Add(-time.Hour))
	createWorkspaceContractGrant(t, database, "workspace-other", "user-2", otherRoot, StatusActive, 1, now.Add(time.Hour))

	for _, query := range []string{"quarterly", strings.ToLower(filepath.Base(projectRoot))} {
		request := httptest.NewRequest(http.MethodGet, "/local-workspaces?include_inactive=true&query="+url.QueryEscape(query), nil)
		request.Header.Set("X-User-Id", "user-1")
		response := httptest.NewRecorder()
		List(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("query %q status=%d body=%s", query, response.Code, response.Body.String())
		}
		var data struct {
			Items []PublicWorkspace `json:"items"`
		}
		decodeWorkspaceContractData(t, response, &data)
		if len(data.Items) != 1 || data.Items[0].WorkspaceID != project.ID {
			t.Fatalf("query %q returned %#v", query, data.Items)
		}
	}
}

func TestRevokeReturnsAffectedTaskCountAndBlocksEveryActor(t *testing.T) {
	database := newWorkspaceHandlerContractDB(t)
	workspace := createWorkspaceContractGrant(t, database, "workspace-revoke", "user-1", t.TempDir(), StatusActive, 7, time.Now().UTC())
	for _, conversationID := range []string{"task-one", "task-two"} {
		createWorkspaceContractConversation(t, database, conversationID, "user-1")
		bindWorkspaceContractConversation(t, database, conversationID, workspace.ID)
	}

	request := httptest.NewRequest(http.MethodPost, "/local-workspaces/"+workspace.ID+":revoke", bytes.NewBufferString(`{"version":7}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Id", "user-1")
	request = mux.SetURLVars(request, map[string]string{"workspace_id": workspace.ID})
	response := httptest.NewRecorder()
	Revoke(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Status            string `json:"status"`
		Version           int64  `json:"version"`
		AffectedTaskCount int64  `json:"affected_task_count"`
	}
	decodeWorkspaceContractData(t, response, &result)
	if result.Status != StatusRevoked || result.Version != 8 || result.AffectedTaskCount != 2 {
		t.Fatalf("unexpected revoke result: %#v", result)
	}

	for _, actor := range []struct{ actorType, actorID string }{
		{"main_agent", "main"}, {"sub_agent", "subtask-1"}, {"skill", "skill-1"},
	} {
		body, err := json.Marshal(map[string]any{
			"conversation_id": "task-one", "execution_id": "execution-1",
			"actor_type": actor.actorType, "actor_id": actor.actorID, "operation_class": "read",
		})
		if err != nil {
			t.Fatal(err)
		}
		resolveRequest := httptest.NewRequest(http.MethodPost, "/internal/local-workspaces:resolve", bytes.NewReader(body))
		resolveRequest.Header.Set("Content-Type", "application/json")
		resolveRequest.Header.Set("X-User-Id", "user-1")
		resolveRequest.Header.Set("X-LazyMind-Local-Workspace-Token", "workspace-contract-host")
		resolveResponse := httptest.NewRecorder()
		InternalResolve(resolveResponse, resolveRequest)
		if resolveResponse.Code != http.StatusConflict {
			t.Fatalf("actor=%s status=%d body=%s", actor.actorType, resolveResponse.Code, resolveResponse.Body.String())
		}
		if strings.Contains(resolveResponse.Body.String(), workspace.CanonicalPath) {
			t.Fatalf("actor=%s revoke error leaked root: %s", actor.actorType, resolveResponse.Body.String())
		}
	}
}

func TestRegisterAfterRevocationCreatesNewGrantWithoutRestoringOldTask(t *testing.T) {
	database := newWorkspaceHandlerContractDB(t)
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := currentDirectoryIdentity(canonical)
	if err != nil {
		t.Fatal(err)
	}
	input := RegisterInput{
		DisplayName: filepath.Base(canonical), CanonicalPath: canonical,
		DirectoryIdentity: identity, Source: "local",
	}
	first, err := Register(t.Context(), database.DB, "user-1", input)
	if err != nil {
		t.Fatalf("register first grant: %v", err)
	}
	createWorkspaceContractConversation(t, database, "task-old-grant", "user-1")
	bindWorkspaceContractConversation(t, database, "task-old-grant", first.WorkspaceID)
	if err := database.Model(&orm.LocalWorkspace{}).Where("id = ?", first.WorkspaceID).Updates(map[string]any{
		"status": StatusRevoked, "version": 2, "revoked_at": time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	second, err := Register(t.Context(), database.DB, "user-1", input)
	if err != nil {
		t.Fatalf("register replacement grant: %v", err)
	}
	if second.WorkspaceID == first.WorkspaceID {
		t.Fatalf("revoked grant %s was revived", first.WorkspaceID)
	}
	var binding orm.ConversationWorkspaceBinding
	if err := database.Where("conversation_id = ?", "task-old-grant").First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if binding.WorkspaceID != first.WorkspaceID {
		t.Fatalf("old task binding changed to %s", binding.WorkspaceID)
	}
	if _, err := ResolveActiveForBinding(t.Context(), database.DB, "user-1", binding.WorkspaceID); err == nil {
		t.Fatal("old task unexpectedly regained workspace access")
	}
}

func TestUpdateConversationPermissionUsesOptimisticVersion(t *testing.T) {
	database := newWorkspaceHandlerContractDB(t)
	workspace := createWorkspaceContractGrant(t, database, "workspace-permission", "user-1", t.TempDir(), StatusActive, 1, time.Now().UTC())
	createWorkspaceContractConversation(t, database, "task-permission", "user-1")
	bindWorkspaceContractConversation(t, database, "task-permission", workspace.ID)

	update := func(version int64, mode string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]any{"permission_mode": mode, "version": version})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPut, "/conversations/task-permission:workspace-permission", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-User-Id", "user-1")
		request = mux.SetURLVars(request, map[string]string{"conversation_id": "task-permission"})
		response := httptest.NewRecorder()
		UpdateConversationPermission(response, request)
		return response
	}

	first := update(1, PermissionAllowAll)
	if first.Code != http.StatusOK {
		t.Fatalf("first update status=%d body=%s", first.Code, first.Body.String())
	}
	stale := update(1, PermissionAlwaysAsk)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
	var binding orm.ConversationWorkspaceBinding
	if err := database.Where("conversation_id = ?", "task-permission").First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if binding.PermissionMode != PermissionAllowAll || binding.PermissionVersion != 2 {
		t.Fatalf("stale update changed binding: %#v", binding)
	}
}
