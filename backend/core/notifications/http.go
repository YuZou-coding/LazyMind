package notifications

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"gorm.io/gorm"
	"lazymind/core/common"
	"lazymind/core/common/orm"
	"lazymind/core/store"
)

func ReplyError(w http.ResponseWriter, r *http.Request, err error) {
	app := common.NewAppError(500, common.ErrCodeInternal, "通知服务暂时不可用")
	var known *common.AppError
	if errors.As(err, &known) {
		app = known
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		app = common.NewAppError(404, common.ErrCodeResourceAbsent, "资源不存在")
	} else if errors.Is(err, ErrInvalid) {
		app = ErrInvalid
	}
	id := r.Header.Get("X-Request-Id")
	if id == "" || len(id) > 128 {
		id = common.GenerateID()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(app.HTTPStatus)
	message := common.NotificationErrorMessage(app.Code, r.Header.Get("Accept-Language"), app.Message)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": app.Code, "message": message, "request_id": id})
}
func owner(w http.ResponseWriter, r *http.Request) string {
	id := store.UserID(r)
	if r.Header.Get("X-User-Id") == "" || id == "" {
		ReplyError(w, r, common.NewAppError(401, common.ErrCodeUnauthorized, "请先登录"))
		return ""
	}
	return id
}
func decode(r *http.Request, dest any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	d.DisallowUnknownFields()
	if d.Decode(dest) != nil {
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func settingsView(row orm.NotificationSettings) SettingsView {
	rule, _ := parseRule(row.DefaultsJSON)
	return SettingsView{row.Enabled, row.Version, rule}
}
func view(row orm.ScheduleNotificationRule) RuleView {
	rule, _ := parseRule(row.RuleJSON)
	return RuleView{row.Version, rule}
}

func SettingsHandler(w http.ResponseWriter, r *http.Request) {
	user := owner(w, r)
	if user == "" {
		return
	}
	db := store.DB().WithContext(r.Context())
	if r.Method == "GET" {
		row, err := Settings(db, user)
		if err != nil {
			ReplyError(w, r, err)
			return
		}
		common.ReplyJSON(w, settingsView(row))
		return
	}
	var body struct {
		SettingsView
		ImpactRevision string `json:"impact_revision"`
	}
	if err := decode(r, &body); err != nil {
		ReplyError(w, r, err)
		return
	}
	current, err := Settings(db, user)
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	previous, err := parseRule(current.DefaultsJSON)
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	if err := validateRuleUpdate(r.Context(), user, body.Defaults, previous, true, body.Enabled); err != nil {
		ReplyError(w, r, err)
		return
	}
	var saved orm.NotificationSettings
	err = Transact(r.Context(), db, func(tx *gorm.DB) error {
		row, err := LockOwner(tx, user)
		if err != nil {
			return err
		}
		if row.Version != body.Version {
			return ErrConflict
		}
		if row.Enabled && !body.Enabled {
			impact, err := disableImpact(tx, user)
			if err != nil {
				return err
			}
			if impact.Revision != body.ImpactRevision {
				return ErrImpact
			}
			row.Generation++
			if err := tx.Model(&orm.TaskNotificationEvent{}).Where("user_id = ? AND status IN ?", user, []string{"pending", "queued", "retrying"}).Updates(map[string]any{"status": "canceled", "revision": gorm.Expr("revision + 1"), "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		row.Enabled = body.Enabled
		row.Version++
		row.DefaultsJSON = encode(body.Defaults)
		row.UpdatedAt = time.Now().UTC()
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		saved = row
		return nil
	})
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	common.ReplyJSON(w, settingsView(saved))
}

type impactView struct {
	Items    []map[string]any `json:"items"`
	Revision string           `json:"impact_revision"`
}

func disableImpact(db *gorm.DB, user string) (impactView, error) {
	result := impactView{Items: []map[string]any{}}
	var tasks []orm.TaskCenterTask
	if err := db.Where("user_id = ? AND archived_at IS NULL AND status NOT IN ?", user, []string{"succeeded", "failed", "canceled", "skipped"}).Order("id").Find(&tasks).Error; err != nil {
		return result, err
	}
	for _, t := range tasks {
		var snap orm.TaskNotificationSnapshot
		if err := db.First(&snap, "task_id = ?", t.ID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		} else if err != nil {
			return result, err
		}
		rule, err := parseRule(snap.RuleJSON)
		if err != nil {
			return result, err
		}
		for _, ch := range rule.Channels {
			if ch.Enabled {
				result.Items = append(result.Items, map[string]any{"task_id": t.ID, "status": t.Status})
				break
			}
		}
	}
	var events []orm.TaskNotificationEvent
	if err := db.Where("user_id = ? AND status IN ?", user, []string{"pending", "queued", "retrying"}).Order("id").Find(&events).Error; err != nil {
		return result, err
	}
	for _, e := range events {
		result.Items = append(result.Items, map[string]any{"notification_id": e.ID, "task_id": e.TaskID, "revision": e.Revision})
	}
	digest := sha256.Sum256([]byte(encode(result.Items)))
	result.Revision = hex.EncodeToString(digest[:])
	return result, nil
}
func DisableImpactHandler(w http.ResponseWriter, r *http.Request) {
	user := owner(w, r)
	if user == "" {
		return
	}
	var result impactView
	err := Transact(r.Context(), store.DB(), func(tx *gorm.DB) error {
		if _, err := LockOwner(tx, user); err != nil {
			return err
		}
		var err error
		result, err = disableImpact(tx, user)
		return err
	})
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	common.ReplyJSON(w, result)
}

func ReplaceInitialRule(db *gorm.DB, id string, rule Rule) error {
	return db.Model(&orm.ScheduleNotificationRule{}).Where("schedule_id = ?", id).Update("rule_json", encode(rule)).Error
}

func ScheduleRuleHandler(w http.ResponseWriter, r *http.Request) {
	user := owner(w, r)
	if user == "" {
		return
	}
	id := mux.Vars(r)["schedule_id"]
	db := store.DB().WithContext(r.Context())
	row, err := ScheduleRule(db, user, id)
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	if r.Method == "GET" {
		common.ReplyJSON(w, view(row))
		return
	}
	var body RuleView
	if err = decode(r, &body); err != nil {
		ReplyError(w, r, err)
		return
	}
	previous, err := parseRule(row.RuleJSON)
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	if err = validateRuleUpdate(r.Context(), user, body.Rule, previous, false, true); err != nil {
		ReplyError(w, r, err)
		return
	}
	result := db.Model(&orm.ScheduleNotificationRule{}).Where("schedule_id = ? AND user_id = ? AND version = ?", id, user, body.Version).Updates(map[string]any{"rule_json": encode(body.Rule), "version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		ReplyError(w, r, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		ReplyError(w, r, ErrConflict)
		return
	}
	body.Version++
	common.ReplyJSON(w, body)
}
func ResetRuleHandler(w http.ResponseWriter, r *http.Request) {
	user := owner(w, r)
	if user == "" {
		return
	}
	id := mux.Vars(r)["schedule_id"]
	db := store.DB().WithContext(r.Context())
	if _, err := ScheduleRule(db, user, id); err != nil {
		ReplyError(w, r, err)
		return
	}
	var body struct {
		Version int `json:"version"`
	}
	if err := decode(r, &body); err != nil {
		ReplyError(w, r, err)
		return
	}
	settings, err := Settings(db, user)
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	rule, err := parseRule(settings.DefaultsJSON)
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	if err = ValidateRule(r.Context(), user, rule, false); err != nil {
		ReplyError(w, r, err)
		return
	}
	result := db.Model(&orm.ScheduleNotificationRule{}).Where("schedule_id = ? AND user_id = ? AND version = ?", id, user, body.Version).Updates(map[string]any{"rule_json": settings.DefaultsJSON, "version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		ReplyError(w, r, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		ReplyError(w, r, ErrConflict)
		return
	}
	common.ReplyJSON(w, RuleView{body.Version + 1, rule})
}

func HistoryHandler(w http.ResponseWriter, r *http.Request) {
	user := owner(w, r)
	if user == "" {
		return
	}
	db := store.DB().WithContext(r.Context())
	id := mux.Vars(r)["task_id"]
	var task orm.TaskCenterTask
	if err := db.First(&task, "id = ? AND user_id = ?", id, user).Error; err != nil {
		ReplyError(w, r, err)
		return
	}
	var events []orm.TaskNotificationEvent
	if err := db.Where("task_id = ? AND user_id = ?", id, user).Order("created_at,id").Find(&events).Error; err != nil {
		ReplyError(w, r, err)
		return
	}
	type targetView struct {
		orm.TaskNotificationDelivery
		Parts json.RawMessage `json:"parts"`
	}
	type eventView struct {
		orm.TaskNotificationEvent
		Targets []targetView `json:"targets"`
	}
	items := []eventView{}
	for _, event := range events {
		var deliveries []orm.TaskNotificationDelivery
		if err := db.Where("event_id = ?", event.ID).Order("id").Find(&deliveries).Error; err != nil {
			ReplyError(w, r, err)
			return
		}
		item := eventView{TaskNotificationEvent: event, Targets: []targetView{}}
		for _, d := range deliveries {
			parts := json.RawMessage(d.PartsJSON)
			if !json.Valid(parts) {
				parts = json.RawMessage("[]")
			}
			item.Targets = append(item.Targets, targetView{d, parts})
		}
		items = append(items, item)
	}
	common.ReplyJSON(w, map[string]any{"items": items})
}

func internal(w http.ResponseWriter, r *http.Request) bool {
	token := os.Getenv("LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN")
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(r.Header.Get("X-LazyMind-Internal-Token"))) != 1 {
		ReplyError(w, r, common.NewAppError(401, common.ErrCodeUnauthorized, "内部认证失败"))
		return false
	}
	return true
}
func AuthorizeHandler(w http.ResponseWriter, r *http.Request) {
	if !internal(w, r) {
		return
	}
	db := store.DB().WithContext(r.Context())
	id := mux.Vars(r)["notification_id"]
	var event orm.TaskNotificationEvent
	err := db.First(&event, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var d orm.TaskNotificationDelivery
		if err = db.First(&d, "id = ?", id).Error; err == nil {
			err = db.First(&event, "id = ?", d.EventID).Error
		}
	}
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	settings, err := Settings(db, event.UserID)
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	common.ReplyJSON(w, map[string]any{"allowed": settings.Enabled && settings.Generation == event.Generation && event.Status != "canceled", "generation": settings.Generation})
}

func RetryHandler(w http.ResponseWriter, r *http.Request) {
	user := owner(w, r)
	if user == "" {
		return
	}
	id := mux.Vars(r)["notification_id"]
	db := store.DB().WithContext(r.Context())
	var event orm.TaskNotificationEvent
	if err := db.First(&event, "id = ? AND user_id = ?", id, user).Error; err != nil {
		ReplyError(w, r, err)
		return
	}
	var body struct {
		ExpectedRevision     int  `json:"expected_revision"`
		ConfirmDuplicateRisk bool `json:"confirm_duplicate_risk"`
	}
	if err := decode(r, &body); err != nil {
		ReplyError(w, r, err)
		return
	}
	err := Transact(r.Context(), db, func(tx *gorm.DB) error {
		settings, err := LockOwner(tx, user)
		if err != nil {
			return err
		}
		if err = tx.First(&event, "id = ? AND user_id = ?", id, user).Error; err != nil {
			return err
		}
		if !settings.Enabled || settings.Generation != event.Generation || event.Revision != body.ExpectedRevision || (event.Status != "failed" && event.Status != "partial" && event.Status != "unknown") {
			return ErrConflict
		}
		if event.Status == "unknown" && !body.ConfirmDuplicateRisk {
			return ErrDuplicate
		}
		changes := map[string]any{"status": "retrying", "revision": gorm.Expr("revision + 1"), "next_attempt_at": time.Now().UTC(), "updated_at": time.Now().UTC()}
		if event.SummaryStatus == "failed" {
			changes["summary_status"] = "pending"
		}
		if err = tx.Model(&event).Updates(changes).Error; err != nil {
			return err
		}
		// A persisted retry intent survives Core/gateway outages and races.
		status := "retry_requested"
		if body.ConfirmDuplicateRisk {
			status = "retry_confirmed"
		}
		return tx.Model(&orm.TaskNotificationDelivery{}).Where("event_id = ? AND status <> ?", event.ID, "sent").Updates(map[string]any{"status": status, "updated_at": time.Now().UTC()}).Error
	})
	if err != nil {
		ReplyError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	common.ReplyJSON(w, map[string]any{"id": id, "status": "retrying"})
}

// AccountReferencesHandler is the internal read side used by the gateway's
// disconnect impact UI. Ownership comes from its authenticated owner header.
func AccountReferencesHandler(w http.ResponseWriter, r *http.Request) {
	if !internal(w, r) {
		return
	}
	user := owner(w, r)
	if user == "" {
		return
	}
	accountID := mux.Vars(r)["account_id"]
	var rows []orm.ScheduleNotificationRule
	if err := store.DB().WithContext(r.Context()).Where("user_id = ?", user).Find(&rows).Error; err != nil {
		ReplyError(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, row := range rows {
		rule, err := parseRule(row.RuleJSON)
		if err != nil {
			ReplyError(w, r, err)
			return
		}
		found := false
		for _, channel := range rule.Channels {
			for _, target := range channel.Targets {
				found = found || target.AccountID == accountID
			}
		}
		if found {
			var schedule orm.UserSchedule
			if err := store.DB().WithContext(r.Context()).First(&schedule, "id = ? AND user_id = ?", row.ScheduleID, user).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			} else if err != nil {
				ReplyError(w, r, err)
				return
			}
			items = append(items, map[string]any{"schedule_id": schedule.ID, "name": schedule.Name, "enabled": schedule.Enabled})
		}
	}
	common.ReplyJSON(w, map[string]any{"items": items})
}
