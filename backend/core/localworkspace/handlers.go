package localworkspace

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"gorm.io/gorm"

	"lazymind/core/common"
	"lazymind/core/common/orm"
	"lazymind/core/store"
)

func rejectUnlessEnabled(w http.ResponseWriter) bool {
	if !Enabled() {
		common.ReplyAppErr(w, ModeError())
		return true
	}
	return false
}

func List(w http.ResponseWriter, r *http.Request) {
	if rejectUnlessEnabled(w) {
		return
	}
	db := store.DB()
	if db == nil {
		common.ReplyErr(w, "store not initialized", http.StatusInternalServerError)
		return
	}
	userID := store.UserID(r)
	pageSize := 8
	if value := strings.TrimSpace(r.URL.Query().Get("page_size")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 8 {
			common.ReplyErr(w, "invalid request", http.StatusBadRequest)
			return
		}
		pageSize = parsed
	}
	includeInactive := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_inactive")), "true")
	query := db.WithContext(r.Context()).Where("create_user_id = ?", userID)
	if !includeInactive {
		query = query.Where("status = ?", StatusActive)
	}
	if keyword := strings.TrimSpace(r.URL.Query().Get("query")); keyword != "" {
		query = query.Where("LOWER(display_name) LIKE ? OR LOWER(canonical_path) LIKE ?", "%"+strings.ToLower(keyword)+"%", "%"+strings.ToLower(keyword)+"%")
	}
	var rows []orm.LocalWorkspace
	if err := query.Order("last_used_at DESC").Limit(pageSize).Find(&rows).Error; err != nil {
		common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		return
	}
	workspaceIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		workspaceIDs = append(workspaceIDs, row.ID)
	}
	taskCounts := make(map[string]int64, len(rows))
	if len(workspaceIDs) > 0 {
		var counts []struct {
			WorkspaceID       string `gorm:"column:workspace_id"`
			AffectedTaskCount int64  `gorm:"column:affected_task_count"`
		}
		if err := db.WithContext(r.Context()).Model(&orm.ConversationWorkspaceBinding{}).
			Select("workspace_id, COUNT(*) AS affected_task_count").
			Where("workspace_id IN ?", workspaceIDs).
			Group("workspace_id").Scan(&counts).Error; err != nil {
			common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
			return
		}
		for _, count := range counts {
			taskCounts[count.WorkspaceID] = count.AffectedTaskCount
		}
	}
	items := make([]PublicWorkspace, 0, len(rows))
	for _, row := range rows {
		item := publicWorkspace(row)
		item.AffectedTaskCount = taskCounts[row.ID]
		items = append(items, item)
	}
	common.ReplyOK(w, map[string]any{"items": items})
}

func Revoke(w http.ResponseWriter, r *http.Request) {
	if rejectUnlessEnabled(w) {
		return
	}
	db := store.DB()
	if db == nil {
		common.ReplyErr(w, "store not initialized", http.StatusInternalServerError)
		return
	}
	var body struct {
		Version int64 `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Version < 1 {
		common.ReplyErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	workspaceID := mux.Vars(r)["workspace_id"]
	userID := store.UserID(r)
	now := time.Now().UTC()
	var affected int64
	var nextVersion int64
	err := db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&orm.LocalWorkspace{}).
			Where("id = ? AND create_user_id = ? AND status = ? AND version = ?", workspaceID, userID, StatusActive, body.Version).
			Updates(map[string]any{"status": StatusRevoked, "version": gorm.Expr("version + 1"), "revoked_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var row orm.LocalWorkspace
			if err := tx.Where("id = ? AND create_user_id = ?", workspaceID, userID).First(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				return common.ResolveAppError("local workspace not found", 404)
			} else if err != nil {
				return err
			}
			return common.ResolveAppError("local workspace binding conflict", 409)
		}
		if err := tx.Model(&orm.ConversationWorkspaceBinding{}).
			Where("workspace_id = ?", workspaceID).Count(&affected).Error; err != nil {
			return err
		}
		nextVersion = body.Version + 1
		return nil
	})
	if err != nil {
		if appErr, ok := err.(*common.AppError); ok {
			common.ReplyAppErr(w, appErr)
		} else {
			common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	common.ReplyOK(w, map[string]any{
		"workspace_id": workspaceID, "status": StatusRevoked,
		"version": nextVersion, "affected_task_count": affected,
	})
}

func ConversationBinding(w http.ResponseWriter, r *http.Request) {
	if rejectUnlessEnabled(w) {
		return
	}
	db := store.DB()
	if db == nil {
		common.ReplyErr(w, "store not initialized", http.StatusInternalServerError)
		return
	}
	conversationID := mux.Vars(r)["conversation_id"]
	userID := store.UserID(r)
	var conversation orm.Conversation
	if err := db.WithContext(r.Context()).Where("id = ? AND create_user_id = ?", conversationID, userID).First(&conversation).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		common.ReplyErr(w, "conversation not found", http.StatusNotFound)
		return
	} else if err != nil {
		common.ReplyErr(w, "query conversation failed", http.StatusInternalServerError)
		return
	}
	var row struct {
		orm.ConversationWorkspaceBinding
		orm.LocalWorkspace
	}
	err := db.WithContext(r.Context()).Table("conversation_workspace_bindings AS b").
		Select("b.conversation_id, b.workspace_id, b.permission_mode, b.permission_version, b.created_at, b.updated_at, w.*").
		Joins("JOIN local_workspaces AS w ON w.id = b.workspace_id").
		Where("b.conversation_id = ?", conversationID).Scan(&row).Error
	if err != nil {
		common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if row.WorkspaceID == "" {
		common.ReplyOK(w, BindingView{Status: "none"})
		return
	}
	var affected int64
	if err := db.WithContext(r.Context()).Model(&orm.ConversationWorkspaceBinding{}).
		Where("workspace_id = ?", row.WorkspaceID).Count(&affected).Error; err != nil {
		common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		return
	}
	workspace := publicWorkspace(row.LocalWorkspace)
	permissionMode, permissionVersion := NormalizePermission(row.PermissionMode, row.PermissionVersion)
	common.ReplyOK(w, BindingView{
		Status: workspace.Status, WorkspaceID: row.WorkspaceID, Workspace: &workspace,
		AffectedTaskCount: affected, PermissionMode: permissionMode,
		PermissionVersion: permissionVersion,
	})
}

func UpdateConversationPermission(w http.ResponseWriter, r *http.Request) {
	if rejectUnlessEnabled(w) {
		return
	}
	var body struct {
		PermissionMode string `json:"permission_mode"`
		Version        int64  `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		common.ReplyErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.PermissionMode = strings.TrimSpace(body.PermissionMode)
	if !ValidPermissionMode(body.PermissionMode) || body.Version < 1 {
		common.ReplyErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	db := store.DB()
	if db == nil {
		common.ReplyErr(w, "store not initialized", http.StatusInternalServerError)
		return
	}
	conversationID := mux.Vars(r)["conversation_id"]
	userID := store.UserID(r)
	var workspaceID string
	now := time.Now().UTC()
	err := db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var conversation orm.Conversation
		if err := tx.Where("id = ? AND create_user_id = ?", conversationID, userID).First(&conversation).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return common.ResolveAppError("local workspace not found", 404)
		} else if err != nil {
			return err
		}
		if !conversation.IsTaskConv {
			return ModeError()
		}
		var binding orm.ConversationWorkspaceBinding
		if err := tx.Where("conversation_id = ?", conversationID).First(&binding).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return common.ResolveAppError("local workspace not found", 404)
		} else if err != nil {
			return err
		}
		workspaceID = binding.WorkspaceID
		result := tx.Model(&orm.ConversationWorkspaceBinding{}).
			Where("conversation_id = ? AND permission_version = ?", conversationID, body.Version).
			Updates(map[string]any{
				"permission_mode": body.PermissionMode, "permission_version": gorm.Expr("permission_version + 1"), "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return common.ResolveAppError("local workspace binding conflict", 409)
		}
		return nil
	})
	if err != nil {
		if appErr, ok := err.(*common.AppError); ok {
			common.ReplyAppErr(w, appErr)
		} else {
			common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	common.ReplyOK(w, map[string]any{
		"workspace_id": workspaceID, "permission_mode": body.PermissionMode,
		"permission_version": body.Version + 1,
	})
}

func InternalRegister(w http.ResponseWriter, r *http.Request) {
	if rejectUnlessEnabled(w) || !trustedHost(r) {
		if !Enabled() {
			return
		}
		common.ReplyErr(w, "local workspace selection forbidden", http.StatusForbidden)
		return
	}
	var input RegisterInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&input); err != nil {
		common.ReplyErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	workspace, err := Register(r.Context(), store.DB(), store.UserID(r), input)
	if err != nil {
		if appErr, ok := err.(*common.AppError); ok {
			common.ReplyAppErr(w, appErr)
		} else {
			common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	common.ReplyOK(w, workspace)
}

func InternalPrepareReauthorization(w http.ResponseWriter, r *http.Request) {
	if rejectUnlessEnabled(w) || !trustedHost(r) {
		if !Enabled() {
			return
		}
		common.ReplyErr(w, "local workspace selection forbidden", http.StatusForbidden)
		return
	}
	db := store.DB()
	if db == nil {
		common.ReplyErr(w, "store not initialized", http.StatusInternalServerError)
		return
	}
	workspaceID := strings.TrimSpace(mux.Vars(r)["workspace_id"])
	userID := store.UserID(r)
	var workspace orm.LocalWorkspace
	if err := db.WithContext(r.Context()).Where("id = ? AND create_user_id = ?", workspaceID, userID).First(&workspace).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		common.ReplyAppErr(w, common.ResolveAppError("local workspace not found", 404))
		return
	} else if err != nil {
		common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if workspace.Status == StatusActive {
		common.ReplyAppErr(w, common.ResolveAppError("local workspace binding conflict", 409))
		return
	}
	canonicalPath, displayName, identity, err := currentDirectoryDetails(workspace.CanonicalPath)
	if err != nil || canonicalPath != workspace.CanonicalPath || identity != workspace.DirectoryIdentity {
		common.ReplyAppErr(w, common.ResolveAppError("local workspace path unavailable", 409))
		return
	}
	common.ReplyOK(w, map[string]any{
		"display_name": displayName, "canonical_path": canonicalPath,
		"directory_identity": identity,
	})
}

func currentDirectoryDetails(path string) (string, string, string, error) {
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", "", err
	}
	canonicalPath, err = filepath.Abs(canonicalPath)
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Stat(canonicalPath)
	if err != nil || !info.IsDir() {
		return "", "", "", errors.New("workspace directory unavailable")
	}
	identity, err := currentDirectoryIdentity(canonicalPath)
	if err != nil {
		return "", "", "", err
	}
	return filepath.Clean(canonicalPath), filepath.Base(canonicalPath), identity, nil
}

func InternalResolve(w http.ResponseWriter, r *http.Request) {
	if rejectUnlessEnabled(w) || !trustedHost(r) {
		if !Enabled() {
			return
		}
		common.ReplyErr(w, "local workspace selection forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		ConversationID string `json:"conversation_id"`
		ExecutionID    string `json:"execution_id"`
		ActorType      string `json:"actor_type"`
		ActorID        string `json:"actor_id"`
		OperationClass string `json:"operation_class"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		common.ReplyAppErr(w, common.ResolveAppError("local workspace selection invalid", 400))
		return
	}
	body.ConversationID = strings.TrimSpace(body.ConversationID)
	body.ExecutionID = strings.TrimSpace(body.ExecutionID)
	body.ActorType = strings.TrimSpace(body.ActorType)
	body.ActorID = strings.TrimSpace(body.ActorID)
	body.OperationClass = strings.TrimSpace(body.OperationClass)
	if body.ConversationID == "" || len(body.ConversationID) > 128 ||
		body.ExecutionID == "" || len(body.ExecutionID) > 128 ||
		body.ActorID == "" || len(body.ActorID) > 128 ||
		!allowedActorType(body.ActorType) || !allowedOperationClass(body.OperationClass) {
		common.ReplyAppErr(w, common.ResolveAppError("local workspace selection invalid", 400))
		return
	}
	db := store.DB()
	if db == nil {
		common.ReplyErr(w, "store not initialized", http.StatusInternalServerError)
		return
	}
	userID := store.UserID(r)
	var conversation orm.Conversation
	if err := db.WithContext(r.Context()).Where("id = ? AND create_user_id = ?", body.ConversationID, userID).First(&conversation).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		common.ReplyAppErr(w, common.ResolveAppError("local workspace not found", 404))
		return
	} else if err != nil {
		common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !conversation.IsTaskConv {
		common.ReplyAppErr(w, ModeError())
		return
	}
	var binding orm.ConversationWorkspaceBinding
	if err := db.WithContext(r.Context()).Where("conversation_id = ?", body.ConversationID).First(&binding).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		common.ReplyAppErr(w, common.ResolveAppError("local workspace not found", 404))
		return
	} else if err != nil {
		common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		return
	}
	workspace, err := ResolveActiveForBinding(r.Context(), db, userID, binding.WorkspaceID)
	if err != nil {
		if appErr, ok := err.(*common.AppError); ok {
			common.ReplyAppErr(w, appErr)
		} else {
			common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	if err := ValidateCurrentDirectory(r.Context(), db, workspace); err != nil {
		if appErr, ok := err.(*common.AppError); ok {
			common.ReplyAppErr(w, appErr)
		} else {
			common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	capabilityID, err := newCapabilityID()
	if err != nil {
		common.ReplyErr(w, "internal server error", http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	permissionMode, permissionVersion := NormalizePermission(binding.PermissionMode, binding.PermissionVersion)
	common.ReplyOK(w, map[string]any{
		"capability_id":      capabilityID,
		"workspace_id":       workspace.ID,
		"workspace_version":  workspace.Version,
		"root_path":          workspace.CanonicalPath,
		"execution_id":       body.ExecutionID,
		"actor_type":         body.ActorType,
		"actor_id":           body.ActorID,
		"operation_class":    body.OperationClass,
		"permission_mode":    permissionMode,
		"permission_version": permissionVersion,
		"issued_at":          now,
		"expires_at":         now.Add(30 * time.Second),
	})
}

func allowedActorType(value string) bool {
	switch value {
	case "main_agent", "sub_agent", "skill":
		return true
	default:
		return false
	}
}

func allowedOperationClass(value string) bool {
	switch value {
	case "read", "write", "command", "network", "connected_app":
		return true
	default:
		return false
	}
}

func newCapabilityID() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "lwcap_" + hex.EncodeToString(buf), nil
}

func trustedHost(r *http.Request) bool {
	expected := strings.TrimSpace(os.Getenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN"))
	provided := strings.TrimSpace(r.Header.Get("X-LazyMind-Local-Workspace-Token"))
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}
