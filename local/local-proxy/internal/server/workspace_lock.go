package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/lazyagi/lazymind/local_proxy/internal/config"
)

const workspaceWriteLeaseTTL = 60 * time.Second

type workspaceWriteLease struct {
	id        string
	keys      []string
	expiresAt time.Time
}

type workspaceWriteLockBroker struct {
	mu     sync.Mutex
	now    func() time.Time
	leases map[string]workspaceWriteLease
}

func newWorkspaceWriteLockBroker() *workspaceWriteLockBroker {
	return &workspaceWriteLockBroker{now: time.Now, leases: map[string]workspaceWriteLease{}}
}

func (b *workspaceWriteLockBroker) acquire(key string, wait time.Duration) (workspaceWriteLease, bool) {
	return b.acquireMany([]string{key}, wait)
}

func (b *workspaceWriteLockBroker) acquireMany(keys []string, wait time.Duration) (workspaceWriteLease, bool) {
	deadline := b.now().Add(wait)
	for {
		b.mu.Lock()
		now := b.now()
		for existingKey, lease := range b.leases {
			if !now.Before(lease.expiresAt) {
				delete(b.leases, existingKey)
			}
		}
		available := true
		for _, key := range keys {
			if _, exists := b.leases[key]; exists {
				available = false
				break
			}
		}
		if available {
			leaseBytes := make([]byte, 32)
			if _, err := rand.Read(leaseBytes); err != nil {
				b.mu.Unlock()
				return workspaceWriteLease{}, false
			}
			lease := workspaceWriteLease{
				id: hex.EncodeToString(leaseBytes), keys: append([]string(nil), keys...), expiresAt: now.Add(workspaceWriteLeaseTTL),
			}
			for _, key := range keys {
				b.leases[key] = lease
			}
			b.mu.Unlock()
			return lease, true
		}
		b.mu.Unlock()
		if !b.now().Before(deadline) {
			return workspaceWriteLease{}, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (b *workspaceWriteLockBroker) release(key, leaseID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	lease, ok := b.leases[key]
	if !ok || subtle.ConstantTimeCompare([]byte(lease.id), []byte(leaseID)) != 1 {
		return false
	}
	for _, leaseKey := range lease.keys {
		delete(b.leases, leaseKey)
	}
	return true
}

type workspaceWriteLockHandler struct {
	cfg    config.Config
	broker *workspaceWriteLockBroker
}

type workspaceWriteLockRequest struct {
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceVersion  int64  `json:"workspace_version"`
	PermissionVersion int64  `json:"permission_version"`
	UserID            string `json:"user_id"`
	ConversationID    string `json:"conversation_id"`
	ExecutionID       string `json:"execution_id"`
	ActorType         string `json:"actor_type"`
	ActorID           string `json:"actor_id"`
	RelativePath      string `json:"relative_path"`
	LeaseID           string `json:"lease_id,omitempty"`
}

func (h *workspaceWriteLockHandler) acquire(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		workspaceError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if !validWorkspaceBrokerCaller(r) {
		workspaceError(w, http.StatusForbidden, "LOCAL_EXECUTION_UNAVAILABLE")
		return
	}
	var body workspaceWriteLockRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&body); err != nil || !validWriteLockRequest(body) {
		workspaceError(w, http.StatusBadRequest, "LOCAL_FILE_PATH_FORBIDDEN")
		return
	}
	if !h.resolveWriteCapability(r, body) {
		workspaceError(w, http.StatusForbidden, "LOCAL_WORKSPACE_CAPABILITY_INVALID")
		return
	}
	key := workspaceWriteLockKey(body.WorkspaceID, body.RelativePath)
	taskKey := "task:" + body.ConversationID
	lease, ok := h.broker.acquireMany([]string{taskKey, key}, 30*time.Second)
	if !ok {
		workspaceError(w, http.StatusConflict, "LOCAL_FILE_CONFLICT")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lease_id": lease.id, "expires_at": lease.expiresAt.UTC(),
	})
}

func (h *workspaceWriteLockHandler) release(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !validWorkspaceBrokerCaller(r) {
		workspaceError(w, http.StatusForbidden, "LOCAL_EXECUTION_UNAVAILABLE")
		return
	}
	var body workspaceWriteLockRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&body); err != nil || !validWriteLockRequest(body) || strings.TrimSpace(body.LeaseID) == "" {
		workspaceError(w, http.StatusBadRequest, "LOCAL_FILE_PATH_FORBIDDEN")
		return
	}
	if !h.broker.release(workspaceWriteLockKey(body.WorkspaceID, body.RelativePath), body.LeaseID) {
		workspaceError(w, http.StatusConflict, "LOCAL_FILE_CONFLICT")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"released": true})
}

func validWorkspaceBrokerCaller(r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return false
	}
	expected := strings.TrimSpace(os.Getenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN"))
	provided := strings.TrimSpace(r.Header.Get("X-LazyMind-Local-Workspace-Token"))
	return expected != "" && len(expected) == len(provided) && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func validWriteLockRequest(body workspaceWriteLockRequest) bool {
	relative := strings.ReplaceAll(strings.TrimSpace(body.RelativePath), "\\", "/")
	cleaned := path.Clean(relative)
	return body.WorkspaceID != "" && body.WorkspaceVersion > 0 && body.PermissionVersion > 0 &&
		body.UserID != "" && body.ConversationID != "" && body.ExecutionID != "" &&
		body.ActorID != "" && (body.ActorType == "main_agent" || body.ActorType == "sub_agent" || body.ActorType == "skill") &&
		relative != "" && relative != "." && !strings.HasPrefix(relative, "/") &&
		!strings.Contains(strings.Split(relative, "/")[0], ":") &&
		cleaned == relative && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func workspaceWriteLockKey(workspaceID, relativePath string) string {
	sum := sha256.Sum256([]byte(workspaceID + "\x00" + relativePath))
	return hex.EncodeToString(sum[:])
}

func (h *workspaceWriteLockHandler) resolveWriteCapability(r *http.Request, body workspaceWriteLockRequest) bool {
	payload, _ := json.Marshal(map[string]any{
		"conversation_id": body.ConversationID,
		"execution_id":    body.ExecutionID,
		"actor_type":      body.ActorType,
		"actor_id":        body.ActorID,
		"operation_class": "write",
	})
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.coreURL()+"/internal/local-workspaces:resolve", strings.NewReader(string(payload)))
	if err != nil {
		return false
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Id", body.UserID)
	request.Header.Set("X-LazyMind-Local-Workspace-Token", strings.TrimSpace(os.Getenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN")))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			WorkspaceID       string `json:"workspace_id"`
			WorkspaceVersion  int64  `json:"workspace_version"`
			PermissionVersion int64  `json:"permission_version"`
		} `json:"data"`
	}
	return response.StatusCode == http.StatusOK && json.NewDecoder(response.Body).Decode(&envelope) == nil &&
		envelope.Data.WorkspaceID == body.WorkspaceID && envelope.Data.WorkspaceVersion == body.WorkspaceVersion &&
		envelope.Data.PermissionVersion == body.PermissionVersion
}

func (h *workspaceWriteLockHandler) coreURL() string {
	for _, route := range h.cfg.Routes {
		if strings.TrimSpace(route.Prefix) == "/api/core" && route.Enabled {
			return strings.TrimRight(route.Upstream, "/")
		}
	}
	return "http://127.0.0.1:8001"
}
