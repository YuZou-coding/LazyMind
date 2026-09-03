package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lazyagi/lazymind/local_proxy/internal/config"
)

func TestWorkspaceWriteLockBrokerSerializesSamePath(t *testing.T) {
	broker := newWorkspaceWriteLockBroker()
	first, ok := broker.acquire("same-path", time.Second)
	if !ok {
		t.Fatal("first lock was not acquired")
	}
	acquired := make(chan workspaceWriteLease, 1)
	go func() {
		lease, acquiredOK := broker.acquire("same-path", time.Second)
		if acquiredOK {
			acquired <- lease
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second lock acquired before first release")
	case <-time.After(60 * time.Millisecond):
	}
	if !broker.release("same-path", first.id) {
		t.Fatal("first lock was not released")
	}
	select {
	case second := <-acquired:
		if second.id == first.id {
			t.Fatal("lease IDs must be one-time values")
		}
	case <-time.After(time.Second):
		t.Fatal("second lock did not acquire after release")
	}
}

func TestWorkspaceWriteLockBrokerExpiresAbandonedLease(t *testing.T) {
	broker := newWorkspaceWriteLockBroker()
	now := time.Unix(100, 0)
	broker.now = func() time.Time { return now }
	first, ok := broker.acquire("same-path", 0)
	if !ok {
		t.Fatal("first lock was not acquired")
	}
	now = now.Add(workspaceWriteLeaseTTL + time.Second)
	second, ok := broker.acquire("same-path", 0)
	if !ok || second.id == first.id {
		t.Fatal("expired lease did not release the path")
	}
}

func TestWorkspaceWriteLockBrokerAllowsOnlyOneWritePerTask(t *testing.T) {
	broker := newWorkspaceWriteLockBroker()
	first, ok := broker.acquireMany([]string{"task:one", "path:a"}, time.Second)
	if !ok {
		t.Fatal("first task write was not acquired")
	}
	if _, ok := broker.acquireMany([]string{"task:one", "path:b"}, 0); ok {
		t.Fatal("same task acquired two concurrent writes")
	}
	if _, ok := broker.acquireMany([]string{"task:two", "path:b"}, 0); !ok {
		t.Fatal("independent task/path should be allowed")
	}
	if !broker.release("path:a", first.id) {
		t.Fatal("first task write was not released")
	}
}

func TestWorkspaceWriteLockRequestRejectsTraversal(t *testing.T) {
	base := workspaceWriteLockRequest{
		WorkspaceID: "workspace-1", WorkspaceVersion: 1, PermissionVersion: 1,
		UserID: "user-1", ConversationID: "conversation-1", ExecutionID: "execution-1",
		ActorType: "main_agent", ActorID: "agent-1",
	}
	for _, candidate := range []string{"", ".", "../outside", "dir/../outside", "/absolute", `C:\\outside`, "C:/outside"} {
		base.RelativePath = candidate
		if validWriteLockRequest(base) {
			t.Fatalf("relative path %q was accepted", candidate)
		}
	}
}

func TestWorkspaceWriteLockHandlerRevalidatesCoreIdentity(t *testing.T) {
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN", "host-token")
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-Id") != "user-1" || r.Header.Get("X-LazyMind-Local-Workspace-Token") != "host-token" {
			t.Fatal("trusted identity headers were not forwarded")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"workspace_id": "workspace-1", "workspace_version": 2, "permission_version": 3,
		}})
	}))
	defer core.Close()
	handler := &workspaceWriteLockHandler{
		cfg:    config.Config{Routes: []config.RouteConfig{{Prefix: "/api/core", Upstream: core.URL, Enabled: true}}},
		broker: newWorkspaceWriteLockBroker(),
	}
	body, _ := json.Marshal(workspaceWriteLockRequest{
		WorkspaceID: "workspace-1", WorkspaceVersion: 2, PermissionVersion: 3,
		UserID: "user-1", ConversationID: "conversation-1", ExecutionID: "execution-1",
		ActorType: "main_agent", ActorID: "agent-1", RelativePath: "docs/note.txt",
	})
	request := httptest.NewRequest(http.MethodPost, "/_local/workspace-write-locks:acquire", strings.NewReader(string(body)))
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("X-LazyMind-Local-Workspace-Token", "host-token")
	response := httptest.NewRecorder()
	handler.acquire(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "lease_id") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkspaceWriteLockHandlerRejectsMissingHostToken(t *testing.T) {
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN", "host-token")
	handler := &workspaceWriteLockHandler{broker: newWorkspaceWriteLockBroker()}
	request := httptest.NewRequest(http.MethodPost, "/_local/workspace-write-locks:acquire", strings.NewReader("{}"))
	request.RemoteAddr = "127.0.0.1:32100"
	response := httptest.NewRecorder()
	handler.acquire(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
