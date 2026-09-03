package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lazyagi/lazymind/local_proxy/internal/auth"
	"github.com/lazyagi/lazymind/local_proxy/internal/config"
)

const workspaceCandidateTTL = 5 * time.Minute

var errWorkspacePickerCanceled = errors.New("workspace picker canceled")

type workspaceCandidate struct {
	path      string
	name      string
	identity  string
	userID    string
	expiresAt time.Time
}

type workspaceCandidateStore struct {
	mu    sync.Mutex
	items map[string]workspaceCandidate
	now   func() time.Time
}

func newWorkspaceCandidateStore() *workspaceCandidateStore {
	return &workspaceCandidateStore{items: map[string]workspaceCandidate{}, now: time.Now}
}

func (s *workspaceCandidateStore) put(candidate workspaceCandidate) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteExpiredLocked()
	candidate.expiresAt = s.now().Add(workspaceCandidateTTL)
	s.items[token] = candidate
	return token, nil
}

func (s *workspaceCandidateStore) consume(token, userID string) (workspaceCandidate, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate, ok := s.items[token]
	if !ok {
		return workspaceCandidate{}, "invalid"
	}
	delete(s.items, token)
	if !s.now().Before(candidate.expiresAt) {
		return workspaceCandidate{}, "expired"
	}
	if candidate.userID == "" || candidate.userID != userID {
		return workspaceCandidate{}, "invalid"
	}
	return candidate, ""
}

func (s *workspaceCandidateStore) tokenState(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate, ok := s.items[token]
	if !ok {
		return "invalid"
	}
	if !s.now().Before(candidate.expiresAt) {
		delete(s.items, token)
		return "expired"
	}
	return ""
}

func (s *workspaceCandidateStore) deleteExpiredLocked() {
	now := s.now()
	for token, candidate := range s.items {
		if !now.Before(candidate.expiresAt) {
			delete(s.items, token)
		}
	}
}

type workspaceHandler struct {
	cfg      config.Config
	sessions *auth.AdminSessionManager
	store    *workspaceCandidateStore
	pick     func(context.Context) (string, error)
	client   *http.Client
}

func newWorkspaceHandler(cfg config.Config) *workspaceHandler {
	return &workspaceHandler{
		cfg: cfg, sessions: auth.NewAdminSessionManager(cfg.Auth.AuthServiceURL, nil),
		store: newWorkspaceCandidateStore(), pick: pickWorkspaceDirectory,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (h *workspaceHandler) selectWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		workspaceError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if code := h.requestBoundaryError(r); code != "" {
		workspaceError(w, http.StatusForbidden, code)
		return
	}
	session, err := h.sessions.Ensure(r.Context(), false)
	if err != nil || session == nil || workspaceSessionUserID(session) == "" {
		workspaceError(w, http.StatusForbidden, "LOCAL_WORKSPACE_SELECTION_FORBIDDEN")
		return
	}
	selectedPath, err := h.pick(r.Context())
	if errors.Is(err, errWorkspacePickerCanceled) {
		writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
		return
	}
	if err != nil {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_PATH_INVALID")
		return
	}
	canonicalPath, displayName, identity, err := validateWorkspaceDirectory(selectedPath)
	if err != nil {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_PATH_INVALID")
		return
	}
	token, err := h.store.put(workspaceCandidate{
		path: canonicalPath, name: displayName, identity: identity,
		userID: workspaceSessionUserID(session),
	})
	if err != nil {
		workspaceError(w, http.StatusServiceUnavailable, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"canceled": false, "selection_token": token, "display_name": displayName,
		"path": canonicalPath, "expires_in_seconds": int(workspaceCandidateTTL.Seconds()),
	})
}

func (h *workspaceHandler) authorizeWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		workspaceError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if code := h.requestBoundaryError(r); code != "" {
		workspaceError(w, http.StatusForbidden, code)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	if len(body) != 1 {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	token, ok := body["selection_token"].(string)
	token = strings.TrimSpace(token)
	if !ok || token == "" {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	if state := h.store.tokenState(token); state == "expired" {
		workspaceError(w, http.StatusGone, "LOCAL_WORKSPACE_SELECTION_EXPIRED")
		return
	} else if state != "" {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	session, err := h.sessions.Ensure(r.Context(), false)
	if err != nil || session == nil || workspaceSessionUserID(session) == "" {
		workspaceError(w, http.StatusForbidden, "LOCAL_WORKSPACE_SELECTION_FORBIDDEN")
		return
	}
	candidate, consumeError := h.store.consume(token, workspaceSessionUserID(session))
	if consumeError == "expired" {
		workspaceError(w, http.StatusGone, "LOCAL_WORKSPACE_SELECTION_EXPIRED")
		return
	}
	if consumeError != "" {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	canonicalPath, displayName, identity, err := validateWorkspaceDirectory(candidate.path)
	if err != nil || canonicalPath != candidate.path || identity != candidate.identity {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_PATH_INVALID")
		return
	}
	workspace, err := h.registerWithCore(r.Context(), session, map[string]string{
		"display_name": displayName, "canonical_path": canonicalPath,
		"directory_identity": identity, "source": "local",
	})
	if err != nil {
		workspaceError(w, http.StatusServiceUnavailable, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	writeJSON(w, http.StatusOK, workspace)
}

func (h *workspaceHandler) reauthorizeWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		workspaceError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if code := h.requestBoundaryError(r); code != "" {
		workspaceError(w, http.StatusForbidden, code)
		return
	}
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	body.WorkspaceID = strings.TrimSpace(body.WorkspaceID)
	if body.WorkspaceID == "" || len(body.WorkspaceID) > 128 {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	session, err := h.sessions.Ensure(r.Context(), false)
	if err != nil || session == nil || workspaceSessionUserID(session) == "" {
		workspaceError(w, http.StatusForbidden, "LOCAL_WORKSPACE_SELECTION_FORBIDDEN")
		return
	}
	candidate, err := h.prepareReauthorizationWithCore(r.Context(), session, body.WorkspaceID)
	if err != nil {
		workspaceError(w, http.StatusBadRequest, "LOCAL_WORKSPACE_PATH_INVALID")
		return
	}
	token, err := h.store.put(workspaceCandidate{
		path: candidate["canonical_path"], name: candidate["display_name"],
		identity: candidate["directory_identity"], userID: workspaceSessionUserID(session),
	})
	if err != nil {
		workspaceError(w, http.StatusServiceUnavailable, "LOCAL_WORKSPACE_SELECTION_INVALID")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"canceled": false, "selection_token": token,
		"display_name": candidate["display_name"], "path": candidate["canonical_path"],
		"expires_in_seconds": int(workspaceCandidateTTL.Seconds()),
	})
}

func (h *workspaceHandler) requestBoundaryError(r *http.Request) string {
	if h.cfg.Auth.AutoLoginAllowLAN || !isLoopbackHost(strings.TrimSpace(h.cfg.Listen.Host)) {
		return "LOCAL_WORKSPACE_MODE_FORBIDDEN"
	}
	if !requestFromLoopback(r) {
		return "LOCAL_WORKSPACE_SELECTION_FORBIDDEN"
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || !originAllowed(origin, h.cfg.CORS.AllowedOrigins) {
		return "LOCAL_WORKSPACE_SELECTION_FORBIDDEN"
	}
	return ""
}

func originAllowed(origin string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == origin {
			return true
		}
	}
	return false
}

func workspaceSessionUserID(session *auth.AdminSession) string {
	if session == nil {
		return ""
	}
	if value := strings.TrimSpace(session.UserID); value != "" {
		return value
	}
	return strings.TrimSpace(session.Username)
}

func validateWorkspaceDirectory(path string) (string, string, string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", "", "", errors.New("invalid path")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", "", err
	}
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.IsDir() {
		return "", "", "", errors.New("not a directory")
	}
	identity, err := workspaceDirectoryIdentity(realPath, info)
	if err != nil {
		return "", "", "", err
	}
	return filepath.Clean(realPath), filepath.Base(realPath), identity, nil
}

func (h *workspaceHandler) registerWithCore(ctx context.Context, session *auth.AdminSession, input map[string]string) (map[string]any, error) {
	coreURL := h.coreURL()
	if coreURL == "" {
		return nil, errors.New("core route unavailable")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+"/internal/local-workspaces", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	h.applyCoreIdentityHeaders(req, session)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("core registration failed")
	}
	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code != 0 || envelope.Data == nil {
		return nil, errors.New("invalid core registration response")
	}
	return envelope.Data, nil
}

func (h *workspaceHandler) coreURL() string {
	coreURL := ""
	for _, route := range h.cfg.Routes {
		if route.Name == "core-route" || route.Prefix == "/api/core" {
			coreURL = strings.TrimRight(route.Upstream, "/")
			break
		}
	}
	return coreURL
}

func (h *workspaceHandler) applyCoreIdentityHeaders(req *http.Request, session *auth.AdminSession) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", workspaceSessionUserID(session))
	req.Header.Set("X-User-Name", session.Username)
	req.Header.Set("X-LazyMind-Local-Workspace-Token", strings.TrimSpace(os.Getenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN")))
}

func (h *workspaceHandler) prepareReauthorizationWithCore(ctx context.Context, session *auth.AdminSession, workspaceID string) (map[string]string, error) {
	coreURL := h.coreURL()
	if coreURL == "" {
		return nil, errors.New("core route unavailable")
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		coreURL+"/internal/local-workspaces/"+url.PathEscape(workspaceID)+":select",
		http.NoBody,
	)
	if err != nil {
		return nil, err
	}
	h.applyCoreIdentityHeaders(req, session)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("core reauthorization failed")
	}
	var envelope struct {
		Code int               `json:"code"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code != 0 || envelope.Data == nil {
		return nil, errors.New("invalid core reauthorization response")
	}
	if envelope.Data["canonical_path"] == "" || envelope.Data["directory_identity"] == "" || envelope.Data["display_name"] == "" {
		return nil, errors.New("incomplete core reauthorization response")
	}
	return envelope.Data, nil
}

func workspaceError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"code": code, "message": workspaceErrorMessage(code)})
}

func workspaceErrorMessage(code string) string {
	switch code {
	case "LOCAL_WORKSPACE_MODE_FORBIDDEN":
		return "Local workspace selection is unavailable in this mode"
	case "LOCAL_WORKSPACE_SELECTION_EXPIRED":
		return "The folder selection has expired"
	case "LOCAL_WORKSPACE_PATH_INVALID":
		return "The selected folder is unavailable"
	case "METHOD_NOT_ALLOWED":
		return "Method not allowed"
	default:
		return "Local workspace selection is not allowed"
	}
}
