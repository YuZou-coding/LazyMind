package chat

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"lazymind/core/common/orm"
	"lazymind/core/localworkspace"
)

var validateSubagentWorkspaceDirectory = localworkspace.ValidateCurrentDirectory

func authoritativeSubagentParams(
	ctx context.Context, db *gorm.DB, conversationID, userID string, raw map[string]any,
) (map[string]any, error) {
	params := make(map[string]any, len(raw))
	for key, value := range raw {
		if key != "parent_agentic_config" {
			params[key] = value
		}
	}
	if db == nil || !localworkspace.Enabled() {
		return params, nil
	}
	var conversation orm.Conversation
	err := db.WithContext(ctx).
		Where("id = ? AND create_user_id = ?", strings.TrimSpace(conversationID), strings.TrimSpace(userID)).
		First(&conversation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !conversation.IsTaskConv) {
		return params, nil
	}
	if err != nil {
		return nil, err
	}
	var binding orm.ConversationWorkspaceBinding
	if err := db.WithContext(ctx).Where("conversation_id = ?", conversation.ID).First(&binding).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return params, nil
	} else if err != nil {
		return nil, err
	}
	workspace, err := localworkspace.ResolveActiveForBinding(ctx, db, userID, binding.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if err := validateSubagentWorkspaceDirectory(ctx, db, workspace); err != nil {
		return nil, err
	}
	permissionMode, permissionVersion := localworkspace.NormalizePermission(
		binding.PermissionMode, binding.PermissionVersion,
	)
	params["parent_agentic_config"] = map[string]any{
		"user_id":                      strings.TrimSpace(userID),
		"conversation_id":              conversation.ID,
		"workspace_permission_mode":    permissionMode,
		"workspace_permission_version": permissionVersion,
		"local_fs_sources": []map[string]any{{
			"source_id":                    "local-workspace:" + workspace.ID,
			"workspace_id":                 workspace.ID,
			"workspace_version":            workspace.Version,
			"workspace_permission_mode":    permissionMode,
			"workspace_permission_version": permissionVersion,
			"paths":                        []string{workspace.CanonicalPath},
			"file_extensions":              []string{"*"},
			"relative_paths":               true,
		}},
	}
	return params, nil
}
