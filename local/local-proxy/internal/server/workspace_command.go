package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lazyagi/lazymind/local_proxy/internal/config"
)

const workspaceCommandOutputLimit = 1024 * 1024

var errWorkspaceContainmentUnavailable = errors.New("workspace command containment unavailable")

type workspaceCommandRequest struct {
	WorkspaceID       string            `json:"workspace_id"`
	WorkspaceVersion  int64             `json:"workspace_version"`
	PermissionVersion int64             `json:"permission_version"`
	UserID            string            `json:"user_id"`
	ConversationID    string            `json:"conversation_id"`
	ExecutionID       string            `json:"execution_id"`
	ActorType         string            `json:"actor_type"`
	ActorID           string            `json:"actor_id"`
	Argv              []string          `json:"argv"`
	CWD               string            `json:"cwd"`
	TimeoutSeconds    int               `json:"timeout_seconds"`
	Env               map[string]string `json:"env,omitempty"`
}

type workspaceCommandResult struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

type boundedCommandBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *boundedCommandBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := workspaceCommandOutputLimit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	if original > remaining {
		b.truncated = true
	}
	return original, nil
}

type workspaceCommandLimiter struct {
	mu     sync.Mutex
	active map[string]int
}

func newWorkspaceCommandLimiter() *workspaceCommandLimiter {
	return &workspaceCommandLimiter{active: map[string]int{}}
}

func (l *workspaceCommandLimiter) acquire(ctx context.Context, taskKey string) bool {
	for {
		l.mu.Lock()
		if l.active[taskKey] < 2 {
			l.active[taskKey]++
			l.mu.Unlock()
			return true
		}
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (l *workspaceCommandLimiter) release(taskKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[taskKey] <= 1 {
		delete(l.active, taskKey)
	} else {
		l.active[taskKey]--
	}
}

type workspaceCommandHandler struct {
	cfg     config.Config
	limiter *workspaceCommandLimiter
	runner  func(context.Context, string, string, []string, map[string]string) (workspaceCommandResult, error)
}

func (h *workspaceCommandHandler) run(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		workspaceError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if !validWorkspaceBrokerCaller(r) {
		workspaceError(w, http.StatusForbidden, "LOCAL_EXECUTION_UNAVAILABLE")
		return
	}
	var body workspaceCommandRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&body); err != nil {
		workspaceError(w, http.StatusBadRequest, "LOCAL_COMMAND_FORBIDDEN")
		return
	}
	if err := validateWorkspaceCommandRequest(body); err != nil {
		workspaceError(w, http.StatusForbidden, "LOCAL_COMMAND_FORBIDDEN")
		return
	}
	root, ok := h.resolveCommandCapability(r, body)
	if !ok {
		workspaceError(w, http.StatusForbidden, "LOCAL_WORKSPACE_CAPABILITY_INVALID")
		return
	}
	cwd, ok := resolveWorkspaceCommandCWD(root, body.CWD)
	if !ok || !validateWorkspaceCommandPaths(root, cwd, body.Argv) {
		workspaceError(w, http.StatusForbidden, "LOCAL_COMMAND_FORBIDDEN")
		return
	}
	taskKey := body.UserID + "\x00" + body.ConversationID
	waitContext, cancelWait := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancelWait()
	if !h.limiter.acquire(waitContext, taskKey) {
		workspaceError(w, http.StatusTooManyRequests, "LOCAL_FILE_RESOURCE_LIMIT")
		return
	}
	defer h.limiter.release(taskKey)
	timeout := time.Duration(body.TimeoutSeconds) * time.Second
	commandContext, cancelCommand := context.WithTimeout(r.Context(), timeout)
	defer cancelCommand()
	runner := h.runner
	if runner == nil {
		runner = runContainedWorkspaceCommand
	}
	result, err := runner(commandContext, root, cwd, body.Argv, body.Env)
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		workspaceError(w, http.StatusGatewayTimeout, "LOCAL_COMMAND_TIMEOUT")
		return
	}
	if errors.Is(err, errWorkspaceContainmentUnavailable) {
		workspaceError(w, http.StatusServiceUnavailable, "LOCAL_EXECUTION_UNAVAILABLE")
		return
	}
	if err != nil && result.ExitCode == 0 {
		workspaceError(w, http.StatusInternalServerError, "LOCAL_FILE_IO_FAILED")
		return
	}
	result.Stdout = redactWorkspaceCommandOutput(result.Stdout, root)
	result.Stderr = redactWorkspaceCommandOutput(result.Stderr, root)
	writeJSON(w, http.StatusOK, result)
}

func validateWorkspaceCommandRequest(body workspaceCommandRequest) error {
	identity := workspaceWriteLockRequest{
		WorkspaceID: body.WorkspaceID, WorkspaceVersion: body.WorkspaceVersion,
		PermissionVersion: body.PermissionVersion, UserID: body.UserID,
		ConversationID: body.ConversationID, ExecutionID: body.ExecutionID,
		ActorType: body.ActorType, ActorID: body.ActorID, RelativePath: "command",
	}
	if !validWriteLockRequest(identity) || len(body.Argv) == 0 || len(body.Argv) > 128 {
		return errors.New("invalid command identity")
	}
	if body.TimeoutSeconds < 1 || body.TimeoutSeconds > 600 {
		return errors.New("invalid timeout")
	}
	total := 0
	for _, item := range body.Argv {
		total += len(item)
		if strings.ContainsRune(item, '\x00') {
			return errors.New("invalid argument")
		}
	}
	if total > 64*1024 || commandPermanentlyDenied(body.Argv) {
		return errors.New("command denied")
	}
	allowedEnv := map[string]bool{"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true, "NO_COLOR": true, "CI": true}
	for name := range body.Env {
		if !allowedEnv[strings.ToUpper(name)] {
			return errors.New("environment denied")
		}
	}
	return nil
}

func commandPermanentlyDenied(argv []string) bool {
	executable := strings.ToLower(filepath.Base(argv[0]))
	denied := map[string]bool{
		"sudo": true, "su": true, "chmod": true, "chown": true, "mkfs": true,
		"dd": true, "shutdown": true, "reboot": true, "poweroff": true,
		"kill": true, "killall": true, "pkill": true, "ssh": true, "scp": true,
	}
	if denied[executable] {
		return true
	}
	if executable == "git" || executable == "git.exe" {
		readOnly := map[string]bool{
			"status": true, "diff": true, "log": true, "show": true, "rev-parse": true,
			"ls-files": true, "ls-tree": true, "cat-file": true, "grep": true,
			"describe": true, "name-rev": true, "for-each-ref": true,
		}
		if len(argv) < 2 || !readOnly[argv[1]] {
			return true
		}
	}
	return false
}

func resolveWorkspaceCommandCWD(root, relative string) (string, bool) {
	relative = strings.ReplaceAll(strings.TrimSpace(relative), "\\", "/")
	if relative == "" {
		relative = "."
	}
	cleaned := path.Clean(relative)
	if strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(strings.Split(cleaned, "/")[0], ":") {
		return "", false
	}
	candidate := filepath.Join(root, filepath.FromSlash(cleaned))
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, real)
	return real, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateWorkspaceCommandPaths(root, cwd string, argv []string) bool {
	for _, value := range argv[1:] {
		if value == "" || strings.HasPrefix(value, "-") || strings.Contains(value, "://") {
			continue
		}
		normalized := strings.ReplaceAll(strings.TrimPrefix(value, "@"), "\\", "/")
		looksLikePath := strings.Contains(normalized, "/") || normalized == "." || normalized == ".."
		if !looksLikePath {
			continue
		}
		if strings.HasPrefix(normalized, "/") || strings.Contains(strings.Split(normalized, "/")[0], ":") {
			return false
		}
		cleaned := path.Clean(normalized)
		if cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned == ".git" || strings.HasPrefix(cleaned, ".git/") {
			return false
		}
		candidate := filepath.Clean(filepath.Join(cwd, filepath.FromSlash(cleaned)))
		rel, err := filepath.Rel(root, candidate)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

func redactWorkspaceCommandOutput(value, root string) string {
	normalized := strings.TrimRight(root, string(filepath.Separator))
	value = strings.ReplaceAll(value, normalized+string(filepath.Separator), "."+string(filepath.Separator))
	return strings.ReplaceAll(value, normalized, ".")
}

func (h *workspaceCommandHandler) resolveCommandCapability(r *http.Request, body workspaceCommandRequest) (string, bool) {
	payload, _ := json.Marshal(map[string]any{
		"conversation_id": body.ConversationID, "execution_id": body.ExecutionID,
		"actor_type": body.ActorType, "actor_id": body.ActorID, "operation_class": "command",
	})
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.coreURL()+"/internal/local-workspaces:resolve", bytes.NewReader(payload))
	if err != nil {
		return "", false
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Id", body.UserID)
	request.Header.Set("X-LazyMind-Local-Workspace-Token", strings.TrimSpace(os.Getenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN")))
	client := &http.Client{Timeout: 6 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			WorkspaceID       string `json:"workspace_id"`
			WorkspaceVersion  int64  `json:"workspace_version"`
			PermissionVersion int64  `json:"permission_version"`
			RootPath          string `json:"root_path"`
		} `json:"data"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&envelope) != nil ||
		envelope.Data.WorkspaceID != body.WorkspaceID || envelope.Data.WorkspaceVersion != body.WorkspaceVersion ||
		envelope.Data.PermissionVersion != body.PermissionVersion || envelope.Data.RootPath == "" {
		return "", false
	}
	root, err := filepath.EvalSymlinks(envelope.Data.RootPath)
	return root, err == nil
}

func (h *workspaceCommandHandler) coreURL() string {
	for _, route := range h.cfg.Routes {
		if strings.TrimSpace(route.Prefix) == "/api/core" && route.Enabled {
			return strings.TrimRight(route.Upstream, "/")
		}
	}
	return "http://127.0.0.1:8001"
}
