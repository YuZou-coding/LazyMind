package notifications

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"lazymind/core/common"
	"lazymind/core/common/orm"
)

type Artifact struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type"`
	SizeBytes  int64  `json:"size_bytes"`
	Revision   int    `json:"revision"`
	Source     string `json:"source,omitempty"`
	InlineText string `json:"inline_text,omitempty"`
}
type Content struct {
	Title          string     `json:"title"`
	ExecutedAt     time.Time  `json:"executed_at"`
	Body           string     `json:"body"`
	Summary        string     `json:"summary"`
	SummaryStatus  string     `json:"summary_status"`
	Reason         string     `json:"reason"`
	PendingActions []string   `json:"pending_actions"`
	Artifacts      []Artifact `json:"artifacts"`
}

// RecordTransition must run in the same transaction as the task state change.
// It never contacts a model or a platform; workers consume the durable event.
func RecordTransition(db *gorm.DB, task orm.TaskCenterTask, previous string) error {
	if task.ScheduleID == nil || *task.ScheduleID == "" || previous == task.Status {
		return nil
	}
	event := ""
	switch task.Status {
	case "succeeded", "failed":
		event = task.Status
	case "waiting":
		event = "paused"
	}
	if event == "" {
		return nil
	}
	// Orphaned historical executions cannot create new notification policy.
	var schedule orm.UserSchedule
	err := db.First(&schedule, "id = ? AND user_id = ?", *task.ScheduleID, task.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	settings, err := LockOwner(db, task.UserID)
	if err != nil {
		return err
	}
	var snapshot orm.TaskNotificationSnapshot
	err = db.First(&snapshot, "task_id = ?", task.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	rule, err := parseRule(snapshot.RuleJSON)
	if err != nil {
		return err
	}
	selected := rule.Events[event]
	if !settings.Enabled || !selected.Enabled {
		return nil
	}
	targets := []orm.TaskNotificationDelivery{}
	for _, ch := range rule.Channels {
		if ch.Enabled {
			for _, t := range ch.Targets {
				targets = append(targets, orm.TaskNotificationDelivery{Provider: ch.Provider, AccountID: t.AccountID, RecipientID: t.RecipientID, ContentMode: selected.ContentMode})
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	content := Content{Title: schedule.Name, ExecutedAt: task.CreatedAt, PendingActions: []string{}, Artifacts: []Artifact{}, SummaryStatus: "pending"}
	if task.Title != nil {
		content.Title = *task.Title
	}
	if task.ScheduledFireAt != nil {
		content.ExecutedAt = *task.ScheduledFireAt
	}
	switch event {
	case "succeeded":
		var output orm.TaskRunOutput
		if err := db.First(&output, "task_id = ? AND output_status = ?", task.ID, "ready").Error; err != nil {
			return err
		}
		content.Body = output.FinalAnswerText
		if err := json.Unmarshal(output.ArtifactManifestJSON, &content.Artifacts); err != nil {
			return err
		}
		// SummaryText is legacy prefix extraction; no reliable provenance exists.
	case "failed":
		content.Reason = "任务执行失败，请回 LazyMind 查看并处理"
		var progress map[string]any
		_ = json.Unmarshal([]byte(task.ProgressJSON), &progress)
		// Only stable, application-owned reasons cross the platform boundary.
		// Provider exception text in legacy progress is never copied verbatim.
		if reason, ok := progress["failure_reason"].(string); ok {
			switch reason {
			case "聊天服务未生成可用结果":
				content.Reason = "任务未生成可用结果，请回 LazyMind 检查后重试"
			case "任务执行超时（超过2小时）", "任务执行超过2小时，未正常完成":
				content.Reason = "任务执行超过2小时，请回 LazyMind 检查后重试"
			case "任务执行被中断":
				content.Reason = "任务执行意外中断，请回 LazyMind 检查后重试"
			}
		}
		if task.DependencyStatus == "no_inputs" {
			content.Reason = "等待结束后仍没有可用的上游结果，请检查依赖任务"
		}
		content.SummaryStatus = "ready"
		content.Summary = content.Reason
	case "paused":
		content.Reason = "任务已暂停，等待处理后可继续"
		content.PendingActions = []string{"请回 LazyMind 查看待确认事项并继续任务"}
		if task.WorkflowSessionID != nil && *task.WorkflowSessionID != "" {
			var session orm.WorkflowSession
			if err := db.First(&session, "id = ?", *task.WorkflowSessionID).Error; err != nil {
				return err
			}
			if session.Status == "stopped" {
				content.Reason = "用户已主动停止任务，进度已保留，可以恢复执行"
				content.PendingActions = []string{"请回 LazyMind 选择继续任务"}
			} else if session.Status == "waiting" {
				content.Reason = "任务正在等待用户确认或补充信息"
			}
		}
		content.Summary = content.Reason
		content.SummaryStatus = "ready"
	}
	now := time.Now().UTC()
	row := orm.TaskNotificationEvent{ID: common.GeneratePrefixedID("ntf_", 36), TaskID: task.ID, UserID: task.UserID, Event: event, Episode: snapshot.Episode + 1, RuleVersion: snapshot.RuleVersion, Generation: settings.Generation, Status: "pending", Revision: 1, ContentJSON: encode(content), SummaryStatus: content.SummaryStatus, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&row).Error; err != nil {
		return err
	}
	if err := db.Model(&snapshot).Update("episode", row.Episode).Error; err != nil {
		return err
	}
	for i := range targets {
		targets[i].ID = common.GeneratePrefixedID("ntd_", 36)
		targets[i].EventID = row.ID
		targets[i].Status = "pending"
		targets[i].Revision = 1
		targets[i].PartsJSON = "[]"
		targets[i].UpdatedAt = now
	}
	return db.Create(&targets).Error
}
