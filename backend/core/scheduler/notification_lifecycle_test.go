package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"lazymind/core/common"
	"lazymind/core/common/orm"
)

func notificationOutputFixture(t *testing.T, taskStatus, sessionStatus string) (*gorm.DB, orm.TaskCenterTask) {
	t.Helper()
	db := orm.MigrateTestDB(t, &orm.TaskCenterTask{}, &orm.UserSchedule{}, &orm.TaskRunOutput{}, &orm.ChatHistory{}, &orm.ConversationArtifact{}, &orm.SubAgentTask{}, &orm.SubAgentArtifact{}, &orm.WorkflowSession{})
	now := time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)
	scheduleID, sessionID := "notification-schedule", "notification-session"
	task := orm.TaskCenterTask{ID: "notification-run", UserID: "owner", ConversationID: "notification-conv", ScheduleID: &scheduleID, TaskType: "scheduled", Status: taskStatus, CreatedAt: now, UpdatedAt: now}
	if sessionStatus != "" {
		task.WorkflowSessionID = &sessionID
		if err := db.Create(&orm.WorkflowSession{ID: sessionID, ConversationID: task.ConversationID, WorkflowID: "fixture-workflow", Status: sessionStatus, CreateUserID: "owner", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orm.ChatHistory{ID: "notification-history", Seq: 1, ConversationID: task.ConversationID, Result: "已经生成的阶段性内容"}).Error; err != nil {
		t.Fatal(err)
	}
	return db.DB, task
}

func TestNotificationScheduledAndDependencyDispatchPersistSuccessEvents(t *testing.T) {
	db := orm.MigrateAllModelsForTest(t).DB
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ConversationID string `json:"conversation_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			return
		}
		if err := db.Create(&orm.ChatHistory{ID: common.GeneratePrefixedID("hist_", 36), Seq: 1, ConversationID: request.ConversationID, Result: "已持久化的最终结论"}).Error; err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(server.Close)
	t.Setenv("LAZYMIND_CORE_SELF_URL", server.URL)
	now := time.Now().UTC()
	newSchedule := func(name string, due time.Time) orm.UserSchedule {
		s := orm.UserSchedule{UserID: "owner", Name: name, CronExpr: "0 9 * * *", Timezone: "UTC", PromptTemplate: "执行测试任务", Enabled: true, KbIDs: "[]", FileIDs: "[]"}
		if err := CreateSchedule(t.Context(), db, &s); err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&s).Update("next_run_at", due).Error; err != nil {
			t.Fatal(err)
		}
		s.NextRunAt = due
		rule := `{"events":{"succeeded":{"enabled":true,"content_mode":"full"},"failed":{"enabled":true,"content_mode":"summary"},"paused":{"enabled":true,"content_mode":"summary"}},"channels":[{"provider":"feishu","enabled":true,"targets":[{"account_id":"fixture-account","recipient_id":"fixture-group"}]}]}`
		// Seed an already validated rule; account validation itself is covered by
		// the public API suite. Execute the actual scheduling/finalization path.
		result := db.Table("schedule_notification_rules").Where("schedule_id = ?", s.ID).Updates(map[string]any{"rule_json": rule, "version": 2})
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("notification rule fixture: %v rows=%d", result.Error, result.RowsAffected)
		}
		return s
	}
	awaitSuccess := func(scheduleID string) orm.TaskCenterTask {
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			var task orm.TaskCenterTask
			if err := db.Where("schedule_id = ?", scheduleID).First(&task).Error; err == nil && task.Status == "succeeded" {
				var events []struct {
					Event  string
					TaskID string
				}
				if err := db.Table("task_notification_events").Where("task_id = ?", task.ID).Find(&events).Error; err != nil {
					t.Fatal(err)
				}
				if len(events) != 1 || events[0].Event != "succeeded" {
					t.Fatalf("success state and event did not commit together: %+v", events)
				}
				var output orm.TaskRunOutput
				if err := db.First(&output, "task_id = ?", task.ID).Error; err != nil || output.FinalAnswerText != "已持久化的最终结论" {
					t.Fatalf("missing success output: %v %+v", err, output)
				}
				return task
			}
			select {
			case <-deadline.C:
				t.Fatal("scheduled task did not complete")
			case <-tick.C:
			}
		}
	}
	source := newSchedule("上游", now.Add(-30*time.Minute))
	fireSchedules(t.Context(), db, server.URL)
	awaitSuccess(source.ID)
	target := newSchedule("下游", now.Add(-time.Second))
	if err := replaceDependencies(db, "owner", target.ID, []dependencyInput{{SourceScheduleID: source.ID, IncompletePolicy: "wait_then_run_with_warning", MaxWaitSeconds: 7200}}); err != nil {
		t.Fatal(err)
	}
	fireSchedules(t.Context(), db, server.URL)
	var waiting orm.TaskCenterTask
	if err := db.Where("schedule_id = ?", target.ID).First(&waiting).Error; err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "waiting_inputs" {
		t.Fatalf("dependency status=%s", waiting.Status)
	}
	var count int64
	if err := db.Table("task_notification_events").Where("task_id = ?", waiting.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("upstream waiting generated event: %d %v", count, err)
	}
	resumeWaitingTasks(t.Context(), db)
	completed := awaitSuccess(target.ID)
	if completed.ID != waiting.ID {
		t.Fatal(fmt.Sprintf("dependency run identity changed: %s to %s", waiting.ID, completed.ID))
	}
}

func TestNotificationFinalizerDoesNotTurnPausedRunIntoSuccess(t *testing.T) {
	for _, state := range []struct{ name, task, session string }{
		{"persisted-pause", "waiting", ""},
		{"workflow-waiting", "running", "waiting"},
		{"user-stopped-resumable", "waiting", "waiting"},
		{"workflow-still-running", "running", "active"},
	} {
		t.Run(state.name, func(t *testing.T) {
			db, task := notificationOutputFixture(t, state.task, state.session)
			finalizeTaskOutput(t.Context(), db, task.ID, task.ConversationID)
			var saved orm.TaskCenterTask
			if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Status == "succeeded" || saved.FinishedAt != nil {
				t.Fatalf("unfinished run became terminal: %+v", saved)
			}
		})
	}
}

func TestNotificationFinalizerPreservesCancellationAndFailure(t *testing.T) {
	for _, status := range []string{"canceled", "failed"} {
		t.Run(status, func(t *testing.T) {
			db, task := notificationOutputFixture(t, status, "")
			finalizeTaskOutput(t.Context(), db, task.ID, task.ConversationID)
			var saved orm.TaskCenterTask
			if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Status != status {
				t.Fatalf("late stream close overwrote %s with %s", status, saved.Status)
			}
		})
	}
}

func TestNotificationFinalizerCannotSucceedWhenOutputWriteFails(t *testing.T) {
	db, task := notificationOutputFixture(t, "running", "")
	// Fault injection is at the database boundary, not in notification logic.
	name := "notification_fixture_reject_output"
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_run_outputs" {
			tx.AddError(errors.New("fixture: storage write rejected"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
	finalizeTaskOutput(t.Context(), db, task.ID, task.ConversationID)
	var saved orm.TaskCenterTask
	if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status == "succeeded" {
		t.Fatal("success published without durable output")
	}
	var count int64
	if err := db.Model(&orm.TaskRunOutput{}).Where("task_id = ?", task.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("fault fixture did not reject output write")
	}
}

func TestNotificationFinalizerPreservesWholeVisibleAnswer(t *testing.T) {
	db, task := notificationOutputFixture(t, "running", "")
	answer := strings.Repeat("前半部分正文。\n", 600) + "末尾关键结论：费用下降百分之二十。"
	if err := db.Model(&orm.ChatHistory{}).Where("id = ?", "notification-history").Update("result", answer).Error; err != nil {
		t.Fatal(err)
	}
	finalizeTaskOutput(t.Context(), db, task.ID, task.ConversationID)
	var output orm.TaskRunOutput
	if err := db.First(&output, "task_id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if output.FinalAnswerText != answer {
		t.Fatal("full notification source lost the tail of the answer")
	}
	if output.ContentHash == "" {
		t.Fatal("output has no stable snapshot fingerprint")
	}
}

func TestNotificationFinalizerKeepsVisibleArtifacts(t *testing.T) {
	db, task := notificationOutputFixture(t, "running", "")
	now := time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)
	artifact := orm.ConversationArtifact{ID: "report-image", ConversationID: task.ConversationID, HistoryID: "notification-history", Filename: "report.png", Slot: "report", ContentType: "image", Value: json.RawMessage(`{"url":"/static/fixture.png"}`), CreateUserID: "owner", CreatedAt: now}
	if err := db.Create(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	finalizeTaskOutput(t.Context(), db, task.ID, task.ConversationID)
	var output orm.TaskRunOutput
	if err := db.First(&output, "task_id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	var manifest []artifactManifestItem
	if err := json.Unmarshal(output.ArtifactManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 1 || manifest[0].ArtifactID != artifact.ID || manifest[0].Name != "report.png" {
		t.Fatalf("artifact manifest=%+v", manifest)
	}
}
