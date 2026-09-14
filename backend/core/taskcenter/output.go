package taskcenter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"lazymind/core/common"
	"lazymind/core/common/orm"
	"lazymind/core/doc"
	"lazymind/core/notifications"
	"lazymind/core/subagent"
)

// Final result extraction is shared by dispatch and completed-workflow recovery.
var taskOutputProcessBoundaryPattern = regexp.MustCompile(`(?is)</(?:think|tp|trp|tool_call|tool_result)\s*>`)

func OutputBody(result string) string {
	boundaries := taskOutputProcessBoundaryPattern.FindAllStringIndex(result, -1)
	if len(boundaries) > 0 {
		result = result[boundaries[len(boundaries)-1][1]:]
	}
	return strings.TrimSpace(result)
}

type OutputArtifact struct {
	ArtifactID   string `json:"artifact_id"`
	Name         string `json:"name"`
	MIMEType     string `json:"mime_type"`
	SourceTaskID string `json:"source_task_id"`
	Revision     int    `json:"revision"`
	Kind         string `json:"kind"`
	Source       string `json:"source,omitempty"`
	SizeBytes    int64  `json:"size_bytes"`
	InlineText   string `json:"inline_text,omitempty"`
}

func FinalizeTaskOutput(ctx context.Context, db *gorm.DB, taskID, convID string) (string, error) {
	if db == nil {
		return "", gorm.ErrInvalidDB
	}
	var status string
	err := notifications.Transact(ctx, db, func(tx *gorm.DB) error {
		var err error
		status, err = finalizeTaskOutputTransaction(ctx, tx, taskID, convID)
		return err
	})
	return status, err
}

func finalizeTaskOutputTransaction(ctx context.Context, db *gorm.DB, taskID, convID string) (string, error) {
	if err := db.Model(&orm.TaskCenterTask{}).Where("id = ?", taskID).UpdateColumn("updated_at", time.Now().UTC()).Error; err != nil {
		return "", err
	}
	var task orm.TaskCenterTask
	if err := db.First(&task, "id = ?", taskID).Error; err != nil {
		return "", err
	}
	if task.ArchivedAt != nil || task.Status == "failed" || task.Status == "canceled" || task.Status == "skipped" || task.Status == "waiting" || task.Status == "waiting_inputs" {
		return "", nil
	}
	if task.WorkflowSessionID != nil && *task.WorkflowSessionID != "" {
		var session orm.WorkflowSession
		if err := db.First(&session, "id = ?", *task.WorkflowSessionID).Error; err != nil {
			return "", err
		}
		if session.Status != "completed" {
			return "", nil
		}
	}
	var history orm.ChatHistory
	if err := db.WithContext(ctx).Where("conversation_id = ?", convID).Order("seq DESC").First(&history).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	if history.RunStatus != "" && history.RunStatus != "completed" {
		return "", nil
	}
	manifest := make([]OutputArtifact, 0)
	var convArts []orm.ConversationArtifact
	if err := db.WithContext(ctx).Where("conversation_id = ?", convID).Order("created_at ASC").Find(&convArts).Error; err != nil {
		return "", err
	}
	for _, a := range convArts {
		manifest = append(manifest, snapshotArtifacts(a.ID, a.Filename, a.ContentType, taskID, 1, a.Value, "")...)
	}
	if task.WorkflowSessionID != nil && *task.WorkflowSessionID != "" {
		selected, err := selectedWorkflowOutputArtifacts(ctx, db, *task.WorkflowSessionID)
		if err != nil {
			return "", err
		}
		manifest = append(manifest, selected...)
	} else {
		var subArts []struct {
			ID, Slot, ContentType, WorkspacePath string
			Seq                                  int
			Value                                json.RawMessage
		}
		if err := db.WithContext(ctx).Table("sub_agent_artifacts sa").Select("sa.id, sa.slot, sa.content_type, sa.seq, sa.value, st.workspace_path").Joins("JOIN sub_agent_tasks st ON st.id = sa.task_id").Where("st.conversation_id = ? AND sa.hidden = false", convID).Order("sa.created_at ASC, sa.id ASC").Scan(&subArts).Error; err != nil {
			return "", err
		}
		for _, a := range subArts {
			manifest = append(manifest, snapshotArtifacts(a.ID, a.Slot, a.ContentType, taskID, a.Seq, a.Value, a.WorkspacePath)...)
		}
	}
	manifestJSON, _ := json.Marshal(manifest)
	answer := OutputBody(history.Result)
	status := "ready"
	if answer == "" && len(manifest) == 0 {
		status = "empty"
	}
	h := sha256.Sum256(append([]byte(answer), manifestJSON...))
	now := time.Now().UTC()
	summary := answer
	if len([]rune(summary)) > 2000 {
		summary = string([]rune(summary)[:2000]) + "\n[摘要截断，完整内容可从来源任务读取]"
	}
	out := orm.TaskRunOutput{ID: common.GeneratePrefixedID("out_", 36), TaskID: taskID, ConversationID: convID, FinalAnswerText: answer, SummaryText: summary, ArtifactManifestJSON: manifestJSON, OutputStatus: status, ContentHash: hex.EncodeToString(h[:]), CreatedAt: now, UpdatedAt: now}

	var existing orm.TaskRunOutput
	err := db.Where("task_id = ?", taskID).First(&existing).Error
	if err == nil {
		out.ID = existing.ID
		out.CreatedAt = existing.CreatedAt
		err = db.Save(&out).Error
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		err = db.Create(&out).Error
	}
	if err != nil {
		return "", err
	}
	if status == "ready" {
		err = UpdateTaskStatus(ctx, db, taskID, "succeeded")
	} else {
		err = UpdateTaskFailure(ctx, db, taskID, "聊天服务未生成可用结果")
	}
	return status, err
}

// Workflow outputs use the selected revision, including human edits. Reading
// all SubAgent artifacts would deliver superseded attempts and omit edits.
func selectedWorkflowOutputArtifacts(ctx context.Context, db *gorm.DB, sessionID string) ([]OutputArtifact, error) {
	var revisions []orm.WorkflowSlotRevision
	if err := db.WithContext(ctx).Where("session_id = ? AND selected = ? AND validity = ?", sessionID, true, "effective").
		Order("created_at, slot_id, list_index, id").Find(&revisions).Error; err != nil {
		return nil, err
	}
	result := []OutputArtifact{}
	for _, revision := range revisions {
		contentType, workspace, taskID := "", "", ""
		raw := revision.ContentSnapshot
		var err error
		switch {
		case revision.HumanArtifactID != nil:
			var artifact orm.WorkflowHumanArtifact
			err = db.WithContext(ctx).First(&artifact, "id = ? AND session_id = ?", *revision.HumanArtifactID, sessionID).Error
			contentType, raw = artifact.ContentType, artifact.Value
		case revision.ArtifactSeq != nil:
			var artifact struct {
				TaskID, ContentType, WorkspacePath string
				Value                              json.RawMessage
			}
			err = db.WithContext(ctx).Table("sub_agent_artifacts a").
				Select("a.task_id, a.content_type, a.value, t.workspace_path").
				Joins("JOIN plugin_session_steps s ON s.task_id = a.task_id").
				Joins("JOIN sub_agent_tasks t ON t.id = a.task_id").
				Where("s.session_id = ? AND s.step_id = ? AND s.attempt = ? AND a.slot = ? AND a.seq = ? AND a.hidden = ?", sessionID, revision.StepID, revision.Attempt, revision.Slot, *revision.ArtifactSeq, false).
				Take(&artifact).Error
			contentType, workspace, taskID, raw = artifact.ContentType, artifact.WorkspacePath, artifact.TaskID, artifact.Value
		}
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && len(raw) == 0 {
			result = append(result, OutputArtifact{ArtifactID: revision.ID, Name: revision.Slot, Kind: "file", Revision: revision.Revision})
			continue
		}
		if err != nil {
			return nil, err
		}
		if contentType == "" {
			var legacy map[string]any
			_ = json.Unmarshal(raw, &legacy)
			if legacy["path"] != nil || legacy["url"] != nil {
				contentType = "file"
			} else if legacy["paths"] != nil {
				contentType = "file_list"
			}
		}
		result = append(result, snapshotArtifacts(revision.ID, revision.Slot, contentType, taskID, revision.Revision, raw, workspace)...)
	}
	return result, nil
}

// snapshotArtifacts preserves durable references, never expiring signed URLs
// or unrestricted storage paths. A missing file remains a visible failed item.
func snapshotArtifacts(id, name, contentType, taskID string, revision int, raw json.RawMessage, workspace string) []OutputArtifact {
	if revision < 1 {
		revision = 1
	}
	if name == "" {
		name = "任务产物"
	}
	base := OutputArtifact{ArtifactID: id, Name: name, MIMEType: contentType, SourceTaskID: taskID, Revision: revision, Kind: "file"}
	var value map[string]any
	resolved := subagent.ResolveArtifactSnapshotPaths(raw, workspace)
	_ = json.Unmarshal(resolved, &value)
	fileBacked := contentType == "image" || contentType == "file" || contentType == "file_list" || strings.HasPrefix(contentType, "image/")
	if !fileBacked {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			text, _ = value["text"].(string)
			if text == "" {
				text = string(raw)
			}
		}
		base.Name += ".txt"
		base.MIMEType = "text/plain; charset=utf-8"
		base.SizeBytes = int64(len(text))
		base.InlineText = text
		return []OutputArtifact{base}
	}
	paths := []string{}
	if contentType == "file_list" {
		if list, ok := value["paths"].([]any); ok {
			for _, item := range list {
				p, _ := item.(string)
				paths = append(paths, p)
			}
		}
	} else {
		p, _ := value["path"].(string)
		if p == "" {
			p, _ = value["url"].(string)
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		paths = append(paths, "")
	}
	result := make([]OutputArtifact, 0, len(paths))
	for i, path := range paths {
		item := base
		if contentType == "file_list" {
			item.ArtifactID = fmt.Sprintf("%s:%d", id, i)
			if path != "" {
				item.Name = filepath.Base(path)
			}
		}
		item.Source, item.SizeBytes = doc.StaticFileSnapshotMetadata(path)
		if !strings.Contains(item.MIMEType, "/") {
			item.MIMEType = mime.TypeByExtension(strings.ToLower(filepath.Ext(item.Name)))
		}
		if contentType == "image" || strings.HasPrefix(item.MIMEType, "image/") {
			item.Kind = "image"
		}
		result = append(result, item)
	}
	return result
}

// ReconcileCompletedScheduledWorkflows recovers asynchronous completions after
// resume or a Core restart. An active chat checkpoint is never a final result.
func ReconcileCompletedScheduledWorkflows(ctx context.Context, db *gorm.DB) error {
	var tasks []orm.TaskCenterTask
	if err := db.WithContext(ctx).Table("task_center_tasks t").Select("t.*").
		Joins("JOIN plugin_sessions s ON s.id = t.plugin_session_id AND s.create_user_id = t.user_id").
		Where("t.schedule_id IS NOT NULL AND t.archived_at IS NULL AND t.status IN ? AND s.status = ?", []string{"running", "waiting"}, "completed").
		Order("t.updated_at, t.id").Limit(100).Scan(&tasks).Error; err != nil {
		return err
	}
	for _, task := range tasks {
		if err := notifications.Transact(ctx, db, func(tx *gorm.DB) error {
			var session orm.WorkflowSession
			if err := tx.First(&session, "id = ?", *task.WorkflowSessionID).Error; err != nil {
				return err
			}
			if session.Status != "completed" {
				return nil
			}
			if err := UpdateTaskStatus(ctx, tx, task.ID, "running"); err != nil {
				return err
			}
			_, err := FinalizeTaskOutput(ctx, tx, task.ID, task.ConversationID)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
