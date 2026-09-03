package localworkspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"

	"lazymind/core/common"
	"lazymind/core/common/orm"
)

const (
	StatusActive          = "active"
	StatusRevoked         = "revoked"
	StatusPathUnavailable = "path_unavailable"
	ReadPolicyAllow       = "allow"
	WritePolicyAsk        = "ask_before_write"
	WritePolicyAllow      = "allow"
	PermissionAlwaysAsk   = "always_ask"
	PermissionAskAsNeeded = "ask_as_needed"
	PermissionAllowAll    = "allow_all"
)

type PublicWorkspace struct {
	WorkspaceID       string     `json:"workspace_id"`
	DisplayName       string     `json:"display_name"`
	Path              string     `json:"path"`
	Status            string     `json:"status"`
	Version           int64      `json:"version"`
	Source            string     `json:"source"`
	ReadPolicy        string     `json:"read_policy"`
	WritePolicy       string     `json:"write_policy"`
	AffectedTaskCount int64      `json:"affected_task_count"`
	AuthorizedAt      time.Time  `json:"authorized_at"`
	LastUsedAt        time.Time  `json:"last_used_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
}

type BindingView struct {
	Status            string           `json:"status"`
	WorkspaceID       string           `json:"workspace_id,omitempty"`
	Workspace         *PublicWorkspace `json:"workspace,omitempty"`
	AffectedTaskCount int64            `json:"affected_task_count,omitempty"`
	PermissionMode    string           `json:"permission_mode,omitempty"`
	PermissionVersion int64            `json:"permission_version,omitempty"`
}

func ValidPermissionMode(value string) bool {
	switch value {
	case PermissionAlwaysAsk, PermissionAskAsNeeded, PermissionAllowAll:
		return true
	default:
		return false
	}
}

func NormalizePermission(mode string, version int64) (string, int64) {
	if !ValidPermissionMode(mode) {
		mode = PermissionAskAsNeeded
	}
	if version < 1 {
		version = 1
	}
	return mode, version
}

func Runtime() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LAZYMIND_LOCAL_WORKSPACE_RUNTIME"))) {
	case "local":
		return "local"
	case "desktop":
		return "desktop"
	default:
		return "disabled"
	}
}

func Enabled() bool { return Runtime() != "disabled" }

func ModeError() *common.AppError {
	return common.ResolveAppError("local workspace mode forbidden", 403)
}

func publicWorkspace(row orm.LocalWorkspace) PublicWorkspace {
	return PublicWorkspace{
		WorkspaceID: row.ID, DisplayName: row.DisplayName, Path: row.CanonicalPath,
		Status: row.Status, Version: row.Version, Source: row.Source,
		ReadPolicy: row.ReadPolicy, WritePolicy: row.WritePolicy,
		AuthorizedAt: row.AuthorizedAt, LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt,
	}
}

type RegisterInput struct {
	DisplayName       string `json:"display_name"`
	CanonicalPath     string `json:"canonical_path"`
	DirectoryIdentity string `json:"directory_identity"`
	Source            string `json:"source"`
}

func Register(ctx context.Context, db *gorm.DB, userID string, input RegisterInput) (PublicWorkspace, error) {
	if db == nil {
		return PublicWorkspace{}, errors.New("store not initialized")
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.CanonicalPath = strings.TrimSpace(input.CanonicalPath)
	input.DirectoryIdentity = strings.TrimSpace(input.DirectoryIdentity)
	input.Source = strings.ToLower(strings.TrimSpace(input.Source))
	if userID == "" || input.DisplayName == "" || input.CanonicalPath == "" || input.DirectoryIdentity == "" {
		return PublicWorkspace{}, common.ResolveAppError("local workspace path invalid", 400)
	}
	if input.Source != Runtime() || (input.Source != "local" && input.Source != "desktop") {
		return PublicWorkspace{}, ModeError()
	}

	now := time.Now().UTC()
	var result orm.LocalWorkspace
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing orm.LocalWorkspace
		err := tx.Where("create_user_id = ? AND canonical_path = ? AND directory_identity = ? AND status = ?",
			userID, input.CanonicalPath, input.DirectoryIdentity, StatusActive).First(&existing).Error
		if err == nil {
			if err := tx.Model(&existing).Updates(map[string]any{
				"display_name": input.DisplayName, "last_used_at": now, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			existing.DisplayName = input.DisplayName
			existing.LastUsedAt = now
			existing.UpdatedAt = now
			result = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		workspaceID, err := newID()
		if err != nil {
			return err
		}
		result = orm.LocalWorkspace{
			ID: workspaceID, CreateUserID: userID, DisplayName: input.DisplayName,
			CanonicalPath: input.CanonicalPath, DirectoryIdentity: input.DirectoryIdentity,
			Status: StatusActive, Version: 1, Source: input.Source,
			ReadPolicy: ReadPolicyAllow, WritePolicy: WritePolicyAllow,
			AuthorizedAt: now, LastUsedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&result).Error
	})
	return publicWorkspace(result), err
}

func ResolveActiveForBinding(ctx context.Context, tx *gorm.DB, userID, workspaceID string) (orm.LocalWorkspace, error) {
	var workspace orm.LocalWorkspace
	err := tx.WithContext(ctx).Where("id = ? AND create_user_id = ?", workspaceID, userID).First(&workspace).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workspace, common.ResolveAppError("local workspace not found", 404)
	}
	if err != nil {
		return workspace, err
	}
	switch workspace.Status {
	case StatusRevoked:
		return workspace, common.ResolveAppError("local workspace revoked", 409)
	case StatusPathUnavailable:
		return workspace, common.ResolveAppError("local workspace path unavailable", 409)
	case StatusActive:
		return workspace, nil
	default:
		return workspace, common.ResolveAppError("local workspace path unavailable", 409)
	}
}

// ValidateCurrentDirectory detects a deleted, moved, inaccessible, or replaced
// directory before a future file operation can receive an authorized root.
// The transition is one-way: a path becoming available again never revives the
// old authorization record.
func ValidateCurrentDirectory(ctx context.Context, db *gorm.DB, workspace orm.LocalWorkspace) error {
	identity, err := currentDirectoryIdentity(workspace.CanonicalPath)
	if err == nil && identity == workspace.DirectoryIdentity {
		return nil
	}
	if db == nil {
		return errors.New("store not initialized")
	}
	now := time.Now().UTC()
	result := db.WithContext(ctx).Model(&orm.LocalWorkspace{}).
		Where("id = ? AND status = ? AND version = ?", workspace.ID, StatusActive, workspace.Version).
		Updates(map[string]any{
			"status":     StatusPathUnavailable,
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		_, resolveErr := ResolveActiveForBinding(ctx, db, workspace.CreateUserID, workspace.ID)
		if resolveErr != nil {
			return resolveErr
		}
	}
	return common.ResolveAppError("local workspace path unavailable", 409)
}

func newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "lws_" + hex.EncodeToString(buf), nil
}
