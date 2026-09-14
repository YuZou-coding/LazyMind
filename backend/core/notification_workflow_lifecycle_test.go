package main

// Supplemental review tests: exercise the actual workflow status writers,
// rather than injecting a pre-normalized "waiting" task status.
import (
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
	"lazymind/core/common/orm"
	"lazymind/core/workflow"
)

func notificationWorkflowFixture(t *testing.T) (*notificationAPIHarness, orm.TaskCenterTask, orm.WorkflowSession) {
	t.Helper()
	h := newNotificationAPIHarness(t)
	schedule := h.schedule("owner")
	rule := h.rule("owner", schedule.ID)
	rule.Rule = enabledNotificationRule()
	h.saveRule("owner", schedule.ID, rule)
	task := h.run(schedule, "running")
	now := time.Now().UTC()
	session := orm.WorkflowSession{ID: "notification-workflow", ConversationID: task.ConversationID,
		CreateUserID: "owner", WorkflowID: "writer", WorkflowMode: "dynamic", Status: "active",
		StateVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := h.db.Create(&orm.Conversation{ID: task.ConversationID, BaseModel: orm.BaseModel{
		CreateUserID: "owner", CreatedAt: now, UpdatedAt: now,
	}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Model(&task).Update("plugin_session_id", session.ID).Error; err != nil { // workflow-naming: persistence
		t.Fatal(err)
	}
	return h, task, session
}

func assertNotificationWorkflowState(t *testing.T, h *notificationAPIHarness, taskID, want string, events int) {
	t.Helper()
	var stored orm.TaskCenterTask
	if err := h.db.First(&stored, "id = ?", taskID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != want {
		t.Errorf("stored task status = %q, want %q", stored.Status, want)
	}
	visible := decodeNotificationResponse[struct {
		Status string `json:"status"`
	}](t, h.request("GET", "/task-center/tasks/"+taskID, "owner", nil), 200)
	if visible.Status != want {
		t.Errorf("visible task status = %q, want %q", visible.Status, want)
	}
	history := h.history("owner", taskID)
	if len(history.Items) != events {
		t.Errorf("notification count = %d, want %d", len(history.Items), events)
	}
	identities := map[string]bool{}
	for _, item := range history.Items {
		if identities[item.ID] || len(item.Targets) != 2 {
			t.Errorf("event identity/targets invalid: %+v", item)
		}
		identities[item.ID] = true
	}
}

func TestNotificationWorkflowStopResumeThroughHTTP(t *testing.T) {
	h, task, session := notificationWorkflowFixture(t)
	for index, step := range []struct {
		action, command, taskStatus string
		events                      int
	}{
		{"stop", "stop-once", "waiting", 1},
		{"stop", "stop-once", "waiting", 1},  // Retry the identical command.
		{"stop", "stop-again", "waiting", 1}, // Same pause, new command.
		{"resume", "resume-once", "running", 1},
		{"stop", "stop-after-resume", "waiting", 2},
	} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			response := h.request("POST", "/workflow-sessions/"+session.ID+":"+step.action, "owner",
				map[string]any{"command_id": step.command})
			if response.Code != 200 {
				t.Fatalf("workflow command: HTTP %d: %s", response.Code, response.Body.String())
			}
			assertNotificationWorkflowState(t, h, task.ID, step.taskStatus, step.events)
			for _, item := range h.history("owner", task.ID).Items {
				if item.Event != "paused" {
					t.Errorf("recoverable stop generated %q", item.Event)
				}
			}
		})
	}
}

func TestNotificationWorkflowEnginePauseAndFailure(t *testing.T) {
	h, task, session := notificationWorkflowFixture(t)
	for _, step := range []struct {
		sessionStatus, taskStatus string
		events                    int
	}{
		{"waiting", "waiting", 1}, {"waiting", "waiting", 1},
		{"active", "running", 1}, {"waiting", "waiting", 2}, {"failed", "failed", 3},
	} {
		if err := workflow.UpdateSessionStatus(t.Context(), h.db, session.ID, step.sessionStatus); err != nil {
			t.Fatal(err)
		}
		assertNotificationWorkflowState(t, h, task.ID, step.taskStatus, step.events)
	}
	counts := map[string]int{}
	for _, item := range h.history("owner", task.ID).Items {
		counts[item.Event]++
	}
	if counts["paused"] != 2 || counts["failed"] != 1 {
		t.Errorf("workflow notification events = %v", counts)
	}
}

func TestNotificationWorkflowStopRollsBackIfEventCannotPersist(t *testing.T) {
	h, task, session := notificationWorkflowFixture(t)
	const callback = "notification-review:reject-event"
	if err := h.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_notification_events" {
			tx.AddError(errors.New("notification event fixture write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Create().Remove(callback) })
	response := h.request("POST", "/workflow-sessions/"+session.ID+":stop", "owner",
		map[string]any{"command_id": "stop-rollback"})
	if response.Code < 400 {
		t.Errorf("stop without durable event succeeded: HTTP %d", response.Code)
	}
	var stored orm.WorkflowSession
	if err := h.db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "active" || stored.StateVersion != session.StateVersion {
		t.Errorf("rollback retained workflow mutation: status=%s version=%d", stored.Status, stored.StateVersion)
	}
	assertNotificationWorkflowState(t, h, task.ID, "running", 0)
	var commands int64
	if err := h.db.Model(&orm.WorkflowCommand{}).Where("command_id = ?", "stop-rollback").Count(&commands).Error; err != nil {
		t.Fatal(err)
	}
	if commands != 0 {
		t.Error("failed stop retained a completed idempotent command")
	}
}

func TestNotificationWorkflowTerminalCancellationDoesNotPause(t *testing.T) {
	h, task, session := notificationWorkflowFixture(t)
	response := h.request("POST", "/task-center/tasks/"+task.ID+":cancel", "owner", nil)
	if response.Code != 200 {
		t.Fatalf("cancel: HTTP %d: %s", response.Code, response.Body.String())
	}
	// A late interruption callback must never revive a terminal cancellation.
	if err := workflow.UpdateSessionStatus(t.Context(), h.db, session.ID, "waiting"); err != nil {
		t.Fatal(err)
	}
	assertNotificationWorkflowState(t, h, task.ID, "canceled", 0)
}
