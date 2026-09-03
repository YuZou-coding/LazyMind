package localworkspace

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lazymind/core/common/orm"
	"lazymind/core/store"
)

type iterationTwoEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

func newIterationTwoCapabilityFixture(t *testing.T, isWork bool) (orm.LocalWorkspace, string) {
	t.Helper()
	database := orm.MigrateAllModelsForTest(t)
	if err := database.AutoMigrate(&orm.LocalWorkspace{}, &orm.ConversationWorkspaceBinding{}); err != nil {
		t.Fatalf("migrate local workspace fixture: %v", err)
	}
	store.Init(database.DB, nil, nil)
	t.Cleanup(func() { store.Init(nil, nil, nil) })
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME", "local")
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN", "iteration-two-host")

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize workspace root: %v", err)
	}
	identity, err := currentDirectoryIdentity(root)
	if err != nil {
		t.Fatalf("resolve workspace identity: %v", err)
	}
	now := time.Now().UTC()
	workspace := orm.LocalWorkspace{
		ID: "lws_iteration_two", CreateUserID: "user-1", DisplayName: "workspace",
		CanonicalPath: root, DirectoryIdentity: identity, Status: StatusActive, Version: 7,
		Source: "local", ReadPolicy: "allow", WritePolicy: "allow",
		AuthorizedAt: now, LastUsedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&workspace).Error; err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	conversationID := "conversation-iteration-two"
	conversation := orm.Conversation{
		ID: conversationID, DisplayName: "Iteration two", IsTaskConv: isWork,
		BaseModel: orm.BaseModel{
			CreateUserID: "user-1", CreateUserName: "user-1", CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := database.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := database.Create(&orm.ConversationWorkspaceBinding{
		ConversationID: conversationID, WorkspaceID: workspace.ID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("bind workspace: %v", err)
	}
	return workspace, conversationID
}

func iterationTwoResolveRequest(t *testing.T, conversationID, hostToken string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"conversation_id": conversationID,
		"execution_id":    "execution-1",
		"actor_type":      "main_agent",
		"actor_id":        "agent-1",
		"operation_class": "read",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/local-workspaces:resolve", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Id", "user-1")
	request.Header.Set("X-User-Name", "user-1")
	if hostToken != "" {
		request.Header.Set("X-LazyMind-Local-Workspace-Token", hostToken)
	}
	response := httptest.NewRecorder()
	InternalResolve(response, request)
	return response
}

func TestIterationTwoResolveIssuesShortLivedActorBoundCapability(t *testing.T) {
	workspace, conversationID := newIterationTwoCapabilityFixture(t, true)
	before := time.Now().UTC()
	response := iterationTwoResolveRequest(t, conversationID, "iteration-two-host")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want capability response", response.Code, response.Body.String())
	}
	var envelope iterationTwoEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, response.Body.String())
	}
	if envelope.Code != 0 {
		t.Fatalf("code=%d body=%s", envelope.Code, response.Body.String())
	}
	var capability struct {
		CapabilityID     string    `json:"capability_id"`
		WorkspaceID      string    `json:"workspace_id"`
		WorkspaceVersion int64     `json:"workspace_version"`
		RootPath         string    `json:"root_path"`
		ExecutionID      string    `json:"execution_id"`
		ActorType        string    `json:"actor_type"`
		ActorID          string    `json:"actor_id"`
		OperationClass   string    `json:"operation_class"`
		ExpiresAt        time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(envelope.Data, &capability); err != nil {
		t.Fatalf("decode capability: %v data=%s", err, string(envelope.Data))
	}
	if capability.CapabilityID == "" || capability.WorkspaceID != workspace.ID ||
		capability.WorkspaceVersion != workspace.Version || capability.RootPath != workspace.CanonicalPath {
		t.Fatalf("unexpected capability: %#v", capability)
	}
	if capability.ExecutionID != "execution-1" || capability.ActorType != "main_agent" ||
		capability.ActorID != "agent-1" || capability.OperationClass != "read" {
		t.Fatalf("capability lost execution binding: %#v", capability)
	}
	lifetime := capability.ExpiresAt.Sub(before)
	if lifetime <= 0 || lifetime > 31*time.Second {
		t.Fatalf("capability lifetime=%s, want at most 30 seconds", lifetime)
	}
	if strings.Contains(response.Body.String(), workspace.DirectoryIdentity) {
		t.Fatalf("directory identity leaked in capability response: %s", response.Body.String())
	}
}

func TestIterationTwoResolveRejectsMissingTrustedHostToken(t *testing.T) {
	workspace, conversationID := newIterationTwoCapabilityFixture(t, true)
	response := iterationTwoResolveRequest(t, conversationID, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), workspace.CanonicalPath) {
		t.Fatalf("untrusted caller learned workspace root: %s", response.Body.String())
	}
}

func TestIterationTwoResolveRejectsQuickQuestion(t *testing.T) {
	workspace, conversationID := newIterationTwoCapabilityFixture(t, false)
	response := iterationTwoResolveRequest(t, conversationID, "iteration-two-host")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), workspace.CanonicalPath) {
		t.Fatalf("Chat response leaked workspace root: %s", response.Body.String())
	}
}

func TestResolveReturnsServerConfirmedTaskPermissionMode(t *testing.T) {
	_, conversationID := newIterationTwoCapabilityFixture(t, true)
	response := iterationTwoResolveRequest(t, conversationID, "iteration-two-host")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want capability response", response.Code, response.Body.String())
	}
	var envelope iterationTwoEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var capability struct {
		PermissionMode string `json:"permission_mode"`
	}
	if err := json.Unmarshal(envelope.Data, &capability); err != nil {
		t.Fatalf("decode capability: %v", err)
	}
	if capability.PermissionMode != "ask_as_needed" {
		t.Fatalf("permission_mode=%q, want ask_as_needed", capability.PermissionMode)
	}
}
