package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lazyagi/lazymind/local_proxy/internal/config"
)

const (
	workspaceSelectPath    = "/_local/workspaces:select"
	workspaceAuthorizePath = "/_local/workspaces:authorize"
)

func workspaceContractConfig() config.Config {
	return config.Config{
		Listen: config.ListenConfig{Host: "127.0.0.1", Port: 5024},
		Auth: config.AuthConfig{
			Mode:           "local-rbac",
			AuthServiceURL: "http://auth.local",
		},
		CORS: config.CORSConfig{
			AllowedOrigins: []string{"http://localhost:8090"},
		},
	}
}

func workspaceContractRequest(method, path, body, remoteAddr, origin string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	return req
}

func requireWorkspaceContractError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), status)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if payload["code"] != code {
		t.Fatalf("error code=%#v, want %q", payload["code"], code)
	}
}

func TestWorkspaceSelectionEndpointOnlyAllowsPost(t *testing.T) {
	handler := NewHandler(workspaceContractConfig())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, workspaceContractRequest(
		http.MethodGet, workspaceSelectPath, "", "127.0.0.1:50000", "http://localhost:8090",
	))
	requireWorkspaceContractError(t, response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
}

func TestWorkspaceSelectionRejectsNonLoopbackCaller(t *testing.T) {
	handler := NewHandler(workspaceContractConfig())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, workspaceContractRequest(
		http.MethodPost, workspaceSelectPath, `{}`, "192.168.1.20:50000", "http://localhost:8090",
	))
	requireWorkspaceContractError(t, response, http.StatusForbidden, "LOCAL_WORKSPACE_SELECTION_FORBIDDEN")
}

func TestWorkspaceSelectionRejectsCrossSiteOrigin(t *testing.T) {
	handler := NewHandler(workspaceContractConfig())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, workspaceContractRequest(
		http.MethodPost, workspaceSelectPath, `{}`, "127.0.0.1:50000", "https://attacker.example",
	))
	requireWorkspaceContractError(t, response, http.StatusForbidden, "LOCAL_WORKSPACE_SELECTION_FORBIDDEN")
}

func TestWorkspaceSelectionRequiresAnExplicitAllowedOrigin(t *testing.T) {
	handler := NewHandler(workspaceContractConfig())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, workspaceContractRequest(
		http.MethodPost, workspaceSelectPath, `{}`, "127.0.0.1:50000", "",
	))
	requireWorkspaceContractError(t, response, http.StatusForbidden, "LOCAL_WORKSPACE_SELECTION_FORBIDDEN")
}

func TestWorkspaceSelectionRejectsForwardedNonLoopbackCaller(t *testing.T) {
	handler := NewHandler(workspaceContractConfig())
	request := workspaceContractRequest(
		http.MethodPost, workspaceSelectPath, `{}`, "127.0.0.1:50000", "http://localhost:8090",
	)
	request.Header.Set("X-Forwarded-For", "192.168.1.20")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	requireWorkspaceContractError(t, response, http.StatusForbidden, "LOCAL_WORKSPACE_SELECTION_FORBIDDEN")
}

func TestWorkspaceSelectionStaysDisabledForLANProfile(t *testing.T) {
	cfg := workspaceContractConfig()
	cfg.Listen.Host = "0.0.0.0"
	cfg.Auth.AutoLoginAllowLAN = true
	handler := NewHandler(cfg)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, workspaceContractRequest(
		http.MethodPost, workspaceSelectPath, `{}`, "127.0.0.1:50000", "http://localhost:8090",
	))
	requireWorkspaceContractError(t, response, http.StatusForbidden, "LOCAL_WORKSPACE_MODE_FORBIDDEN")
}

func TestWorkspaceAuthorizationRejectsRendererSuppliedPath(t *testing.T) {
	handler := NewHandler(workspaceContractConfig())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, workspaceContractRequest(
		http.MethodPost,
		workspaceAuthorizePath,
		`{"path":"/Users/alice/Documents"}`,
		"127.0.0.1:50000",
		"http://localhost:8090",
	))
	requireWorkspaceContractError(t, response, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
}

func TestWorkspaceAuthorizationRejectsMissingOrTamperedSelectionToken(t *testing.T) {
	handler := NewHandler(workspaceContractConfig())
	for _, body := range []string{
		`{}`,
		`{"selection_token":"tampered-token"}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, workspaceContractRequest(
			http.MethodPost,
			workspaceAuthorizePath,
			body,
			"127.0.0.1:50000",
			"http://localhost:8090",
		))
		requireWorkspaceContractError(t, response, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
	}
}

func TestWorkspaceSelectionStoreDeclaresFiveMinuteSingleUseCandidates(t *testing.T) {
	paths, err := filepath.Glob("workspace*.go")
	if err != nil {
		t.Fatalf("find workspace implementation: %v", err)
	}
	var source strings.Builder
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		source.Write(body)
	}
	implementation := source.String()
	if !regexp.MustCompile(`5\s*\*\s*time\.Minute`).MatchString(implementation) {
		t.Fatal("workspace candidate token TTL must be declared as five minutes")
	}
	if !strings.Contains(implementation, "delete(") && !strings.Contains(implementation, ".Delete(") {
		t.Fatal("workspace candidate consumption must remove the token from the store")
	}
}

func TestWorkspaceSelectionErrorsDoNotLeakCandidatePath(t *testing.T) {
	handler := NewHandler(workspaceContractConfig())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, workspaceContractRequest(
		http.MethodPost,
		workspaceAuthorizePath,
		`{"selection_token":"tampered-/Users/alice/Documents"}`,
		"127.0.0.1:50000",
		"http://localhost:8090",
	))
	requireWorkspaceContractError(t, response, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
	if strings.Contains(response.Body.String(), "/Users/alice/Documents") {
		t.Fatalf("workspace error leaked a local path: %s", response.Body.String())
	}
}
