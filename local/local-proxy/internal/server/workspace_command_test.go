package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lazyagi/lazymind/local_proxy/internal/config"
)

func validCommandFixture() workspaceCommandRequest {
	return workspaceCommandRequest{
		WorkspaceID: "workspace-1", WorkspaceVersion: 1, PermissionVersion: 1,
		UserID: "user-1", ConversationID: "conversation-1", ExecutionID: "execution-1",
		ActorType: "main_agent", ActorID: "agent-1", Argv: []string{"rg", "TODO", "."},
		CWD: ".", TimeoutSeconds: 30,
	}
}

func TestWorkspaceCommandRequestRejectsPrivilegeGitMutationAndEnvironmentEscape(t *testing.T) {
	for _, mutate := range []func(*workspaceCommandRequest){
		func(body *workspaceCommandRequest) { body.Argv = []string{"sudo", "true"} },
		func(body *workspaceCommandRequest) { body.Argv = []string{"git", "add", "."} },
		func(body *workspaceCommandRequest) { body.Env = map[string]string{"HOME": "/tmp/escape"} },
		func(body *workspaceCommandRequest) { body.TimeoutSeconds = 601 },
	} {
		body := validCommandFixture()
		mutate(&body)
		if validateWorkspaceCommandRequest(body) == nil {
			t.Fatalf("unsafe command accepted: %+v", body)
		}
	}
}

func TestWorkspaceCommandPathsRejectOutsideAndGitInternals(t *testing.T) {
	root := t.TempDir()
	for _, argv := range [][]string{
		{"cat", "/etc/passwd"}, {"cat", "../outside"}, {"cat", ".git/config"},
	} {
		if validateWorkspaceCommandPaths(root, root, argv) {
			t.Fatalf("unsafe path accepted: %#v", argv)
		}
	}
}

func TestWorkspaceCommandLimiterAllowsTwoCommandsPerTask(t *testing.T) {
	limiter := newWorkspaceCommandLimiter()
	ctx := context.Background()
	if !limiter.acquire(ctx, "task") || !limiter.acquire(ctx, "task") {
		t.Fatal("first two commands should acquire")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if limiter.acquire(waitCtx, "task") {
		t.Fatal("third concurrent command acquired")
	}
	limiter.release("task")
	if !limiter.acquire(ctx, "task") {
		t.Fatal("slot was not returned")
	}
}

func TestDarwinWorkspaceCommandContainmentBlocksOutsideRead(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS sandbox profile test")
	}
	root := t.TempDir()
	result, err := runContainedWorkspaceCommand(
		context.Background(), root, root,
		[]string{"/usr/bin/python3", "-c", `print(open('/etc/passwd').read())`}, nil,
	)
	if err == nil || strings.Contains(result.Stdout, "root:") {
		t.Fatalf("sandbox escaped: err=%v stdout=%q stderr=%q", err, result.Stdout, result.Stderr)
	}
}

func TestDarwinWorkspaceCommandBoundsOutput(t *testing.T) {
	buffer := &boundedCommandBuffer{}
	payload := strings.Repeat("x", workspaceCommandOutputLimit+1024)
	if _, err := buffer.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if !buffer.truncated || buffer.buffer.Len() != workspaceCommandOutputLimit {
		t.Fatalf("output limit not enforced: size=%d truncated=%v", buffer.buffer.Len(), buffer.truncated)
	}
}

func TestWorkspaceCommandHandlerRevalidatesAndRunsStructuredArgv(t *testing.T) {
	t.Setenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN", "host-token")
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"workspace_id": "workspace-1", "workspace_version": 1,
			"permission_version": 1, "root_path": root,
		}})
	}))
	defer core.Close()
	var gotArgv []string
	handler := &workspaceCommandHandler{
		cfg:     config.Config{Routes: []config.RouteConfig{{Prefix: "/api/core", Upstream: core.URL, Enabled: true}}},
		limiter: newWorkspaceCommandLimiter(),
		runner: func(_ context.Context, gotRoot, gotCWD string, argv []string, _ map[string]string) (workspaceCommandResult, error) {
			if gotRoot != canonicalRoot || gotCWD != canonicalRoot {
				t.Fatalf("root=%q cwd=%q", gotRoot, gotCWD)
			}
			gotArgv = append([]string(nil), argv...)
			return workspaceCommandResult{Stdout: canonicalRoot + "/note.txt", ExitCode: 0}, nil
		},
	}
	body := validCommandFixture()
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, "/_local/workspace-commands:run", strings.NewReader(string(encoded)))
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("X-LazyMind-Local-Workspace-Token", "host-token")
	response := httptest.NewRecorder()
	handler.run(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), canonicalRoot) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Join(gotArgv, " ") != "rg TODO ." {
		t.Fatalf("argv=%#v", gotArgv)
	}
}
