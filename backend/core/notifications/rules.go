// Package notifications owns durable task notification policy and handoff.
// Platform credentials and delivery remain the channel gateway's responsibility.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"lazymind/core/common"
	"lazymind/core/common/orm"
)

type EventRule struct {
	Enabled     bool   `json:"enabled"`
	ContentMode string `json:"content_mode"`
}
type Target struct {
	AccountID   string `json:"account_id"`
	RecipientID string `json:"recipient_id"`
}
type ChannelRule struct {
	Provider string   `json:"provider"`
	Enabled  bool     `json:"enabled"`
	Targets  []Target `json:"targets"`
}
type Rule struct {
	Events   map[string]EventRule `json:"events"`
	Channels []ChannelRule        `json:"channels"`
}
type RuleView struct {
	Version int  `json:"version"`
	Rule    Rule `json:"rule"`
}
type SettingsView struct {
	Enabled  bool `json:"enabled"`
	Version  int  `json:"version"`
	Defaults Rule `json:"defaults"`
}

var (
	ErrInvalid     = common.ResolveAppError("notification configuration invalid", 400)
	ErrConflict    = common.ResolveAppError("notification version conflict", 409)
	ErrUnsupported = common.ResolveAppError("notification provider unsupported", 400)
	ErrImpact      = common.ResolveAppError("notification disable impact changed", 409)
	ErrUnavailable = common.ResolveAppError("notification dependency unavailable", 503)
	ErrDuplicate   = common.ResolveAppError("notification duplicate risk", 409)
)

func DefaultRule() Rule {
	return Rule{Events: map[string]EventRule{"succeeded": {true, "summary"}, "failed": {true, "summary"}, "paused": {false, "summary"}}, Channels: []ChannelRule{{Provider: "feishu", Targets: []Target{}}}}
}
func encode(v any) string { b, _ := json.Marshal(v); return string(b) }
func parseRule(raw string) (Rule, error) {
	var rule Rule
	err := json.Unmarshal([]byte(raw), &rule)
	return rule, err
}

// Transact preserves an enclosing business transaction. SQLite retries must
// restart the outer transaction, never a savepoint holding an obsolete snapshot.
func Transact(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return db.WithContext(ctx).Transaction(fn)
	}
	return common.TransactionWithSQLiteBusyRetry(ctx, db, fn)
}

func Settings(db *gorm.DB, owner string) (orm.NotificationSettings, error) {
	row := orm.NotificationSettings{UserID: owner, Enabled: true, Version: 1, Generation: 1, DefaultsJSON: encode(DefaultRule()), UpdatedAt: time.Now().UTC()}
	if owner == "" {
		return row, ErrInvalid
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return row, err
	}
	err := db.First(&row, "user_id = ?", owner).Error
	return row, err
}

// LockOwner orders switch changes, execution creation and event creation on
// both supported databases, so a fresh disable confirmation cannot race them.
func LockOwner(db *gorm.DB, owner string) (orm.NotificationSettings, error) {
	row, err := Settings(db, owner)
	if err != nil {
		return row, err
	}
	if err = db.Model(&orm.NotificationSettings{}).Where("user_id = ?", owner).UpdateColumn("updated_at", time.Now().UTC()).Error; err != nil {
		return row, err
	}
	err = db.First(&row, "user_id = ?", owner).Error
	return row, err
}

func ValidateRule(ctx context.Context, owner string, rule Rule, global bool) error {
	return validateRuleUpdate(ctx, owner, rule, Rule{}, global, true)
}

// Existing, owned bindings can be retained while switching notifications off,
// including when an account or the gateway is unavailable. New bindings and
// every enabled channel still require fresh remote validation.
func validateRuleUpdate(ctx context.Context, owner string, rule, previous Rule, global, enabled bool) error {
	if len(rule.Events) != 3 || len(rule.Channels) > 8 {
		return ErrInvalid
	}
	anyEvent := false
	for _, event := range []string{"succeeded", "failed", "paused"} {
		r, ok := rule.Events[event]
		if !ok || (r.ContentMode != "summary" && r.ContentMode != "full") {
			return ErrInvalid
		}
		anyEvent = anyEvent || r.Enabled
	}
	seenProviders := map[string]bool{}
	for _, ch := range rule.Channels {
		if ch.Provider != "feishu" {
			return ErrUnsupported
		}
		if seenProviders[ch.Provider] || len(ch.Targets) > 100 || (ch.Enabled && (len(ch.Targets) == 0 || !anyEvent)) {
			return ErrInvalid
		}
		seenProviders[ch.Provider] = true
		seen := map[Target]bool{}
		for _, target := range ch.Targets {
			if strings.TrimSpace(target.AccountID) == "" || strings.TrimSpace(target.RecipientID) == "" || len(target.AccountID) > 256 || len(target.RecipientID) > 256 || seen[target] {
				return ErrInvalid
			}
			seen[target] = true
		}
	}
	if global && !anyEvent {
		return ErrInvalid
	}
	for _, ch := range rule.Channels {
		targets := ch.Targets
		if !enabled || !ch.Enabled {
			retained := map[Target]bool{}
			for _, old := range previous.Channels {
				if old.Provider == ch.Provider {
					for _, target := range old.Targets {
						retained[target] = true
					}
				}
			}
			targets = nil
			for _, target := range ch.Targets {
				if !retained[target] {
					targets = append(targets, target)
				}
			}
		}
		if len(targets) == 0 {
			continue
		}
		var response struct {
			Valid   bool `json:"valid"`
			Targets []struct {
				Target
				Valid bool `json:"valid"`
			} `json:"targets"`
		}
		if err := gatewayRequest(ctx, "POST", "/internal/notification-targets:validate", map[string]any{"user_id": owner, "provider": ch.Provider, "targets": targets}, &response); err != nil {
			return err
		}
		if !response.Valid {
			return ErrInvalid
		}
		for _, wanted := range targets {
			found := false
			for _, got := range response.Targets {
				if got.Target == wanted && got.Valid {
					found = true
					break
				}
			}
			if !found {
				return ErrInvalid
			}
		}
	}
	return nil
}

// CreateScheduleRule is called in the schedule-creation transaction after any
// explicit rule has been validated outside the transaction.
func CreateScheduleRule(db *gorm.DB, s *orm.UserSchedule, explicit *Rule) error {
	settings, err := LockOwner(db, s.UserID)
	if err != nil {
		return err
	}
	raw := settings.DefaultsJSON
	if explicit != nil {
		raw = encode(explicit)
	}
	return db.Create(&orm.ScheduleNotificationRule{ScheduleID: s.ID, UserID: s.UserID, Version: 1, RuleJSON: raw, UpdatedAt: time.Now().UTC()}).Error
}

func ScheduleRule(db *gorm.DB, owner, id string) (orm.ScheduleNotificationRule, error) {
	var schedule orm.UserSchedule
	if err := db.First(&schedule, "id = ? AND user_id = ?", id, owner).Error; err != nil {
		return orm.ScheduleNotificationRule{}, err
	}
	var row orm.ScheduleNotificationRule
	err := db.First(&row, "schedule_id = ? AND user_id = ?", id, owner).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Legacy schedules never inherit new enabled defaults on upgrade/read.
		row = orm.ScheduleNotificationRule{ScheduleID: id, UserID: owner, Version: 1, RuleJSON: encode(DefaultRule()), UpdatedAt: time.Now().UTC()}
		if err = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err == nil {
			err = db.First(&row, "schedule_id = ? AND user_id = ?", id, owner).Error
		}
	}
	return row, err
}

func Snapshot(db *gorm.DB, task *orm.TaskCenterTask) error {
	if task.ScheduleID == nil || *task.ScheduleID == "" {
		return nil
	}
	if _, err := LockOwner(db, task.UserID); err != nil {
		return err
	}
	rule, err := ScheduleRule(db, task.UserID, *task.ScheduleID)
	if err != nil {
		return err
	}
	return db.Create(&orm.TaskNotificationSnapshot{TaskID: task.ID, UserID: task.UserID, RuleVersion: rule.Version, RuleJSON: rule.RuleJSON}).Error
}
