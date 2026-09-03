package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"lazymind/core/common"
	"lazymind/core/common/orm"
	"lazymind/core/localworkspace"
)

func requestedWorkspaceID(raw map[string]any) (string, bool) {
	value, present := raw["workspace_id"]
	if !present {
		return "", false
	}
	workspaceID, ok := value.(string)
	if !ok {
		return "", true
	}
	return strings.TrimSpace(workspaceID), true
}

func requestedWorkspacePermissionMode(raw map[string]any) (string, bool) {
	value, present := raw["workspace_permission_mode"]
	if !present {
		return localworkspace.PermissionAskAsNeeded, false
	}
	mode, ok := value.(string)
	if !ok {
		return "", true
	}
	return strings.TrimSpace(mode), true
}

func validateWorkspaceRequestMode(raw map[string]any) *common.AppError {
	workspaceID, present := requestedWorkspaceID(raw)
	permissionMode, permissionPresent := requestedWorkspacePermissionMode(raw)
	if !present && !permissionPresent {
		return nil
	}
	if !present || workspaceID == "" || len(workspaceID) > 128 ||
		!localworkspace.ValidPermissionMode(permissionMode) {
		return common.ResolveAppError("local workspace selection invalid", 400)
	}
	runInBackground, _ := raw["run_in_background"].(bool)
	if !runInBackground || !localworkspace.Enabled() {
		return localworkspace.ModeError()
	}
	return nil
}

func ensureConversationWithWorkspace(
	ctx context.Context,
	db *gorm.DB,
	convID, displayName string,
	searchConfig, models json.RawMessage,
	userID, userName string,
	runInBackground bool,
	requestedThinkingDepth string,
	conversationSettings map[string]any,
	raw map[string]any,
) (*orm.Conversation, int, error) {
	workspaceID, workspacePresent := requestedWorkspaceID(raw)
	permissionMode, _ := requestedWorkspacePermissionMode(raw)
	var conversation *orm.Conversation
	var seq int
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing orm.Conversation
		existingErr := tx.Where("id = ? AND create_user_id = ?", convID, userID).First(&existing).Error
		conversationExists := existingErr == nil
		if existingErr != nil && !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}

		if conversationExists && workspacePresent && workspaceID != "" {
			if !existing.IsTaskConv {
				return localworkspace.ModeError()
			}
			var binding orm.ConversationWorkspaceBinding
			err := tx.Where("conversation_id = ?", convID).First(&binding).Error
			if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && binding.WorkspaceID != workspaceID) {
				return common.ResolveAppError("local workspace binding locked", 409)
			}
			if err != nil {
				return err
			}
		}

		if !conversationExists && workspacePresent && workspaceID != "" {
			if _, err := localworkspace.ResolveActiveForBinding(ctx, tx, userID, workspaceID); err != nil {
				return err
			}
		}

		created, nextSeq, err := ensureConversation(
			ctx, tx, convID, displayName, searchConfig, models, userID, userName,
			runInBackground, requestedThinkingDepth, conversationSettings,
		)
		if err != nil {
			return err
		}
		conversation = created
		seq = nextSeq

		if !conversationExists && runInBackground {
			if err := tx.Model(&orm.Conversation{}).Where("id = ?", convID).Update("is_task_conv", true).Error; err != nil {
				return err
			}
			conversation.IsTaskConv = true
		}
		if !conversationExists && workspacePresent && workspaceID != "" {
			now := time.Now().UTC()
			binding := orm.ConversationWorkspaceBinding{
				ConversationID: convID, WorkspaceID: workspaceID,
				PermissionMode: permissionMode, PermissionVersion: 1,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&binding).Error; err != nil {
				return common.ResolveAppError("local workspace binding conflict", 409)
			}
			if err := tx.Model(&orm.LocalWorkspace{}).Where("id = ?", workspaceID).
				Updates(map[string]any{"last_used_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return conversation, seq, err
}
