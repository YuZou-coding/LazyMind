package main

// These tests intentionally exercise the registered HTTP surface. Missing
// notification routes are failures, not skips or test-only implementations.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"gorm.io/gorm"
	"lazymind/core/common/orm"
	"lazymind/core/scheduler"
	"lazymind/core/store"
	"lazymind/core/taskcenter"
)

type notificationEventRule struct {
	Enabled     bool   `json:"enabled"`
	ContentMode string `json:"content_mode"`
}
type notificationTarget struct {
	AccountID   string `json:"account_id"`
	RecipientID string `json:"recipient_id"`
}
type notificationChannelRule struct {
	Provider string               `json:"provider"`
	Enabled  bool                 `json:"enabled"`
	Targets  []notificationTarget `json:"targets"`
}
type notificationRule struct {
	Events   map[string]notificationEventRule `json:"events"`
	Channels []notificationChannelRule        `json:"channels"`
}
type notificationSettings struct {
	Enabled  bool             `json:"enabled"`
	Version  int              `json:"version"`
	Defaults notificationRule `json:"defaults"`
}
type notificationRuleView struct {
	Version int              `json:"version"`
	Rule    notificationRule `json:"rule"`
}
type notificationHistory struct {
	Items []struct {
		ID          string               `json:"id"`
		Event       string               `json:"event"`
		Status      string               `json:"status"`
		RuleVersion int                  `json:"rule_version"`
		Targets     []notificationTarget `json:"targets"`
	} `json:"items"`
}
type notificationAPIHarness struct {
	t                *testing.T
	db               *gorm.DB
	router           *mux.Router
	validationMu     sync.Mutex
	validations      int
	validationStatus int
}

func newNotificationAPIHarness(t *testing.T) *notificationAPIHarness {
	t.Helper()
	db := orm.MigrateAllModelsForTest(t)
	store.Init(db.DB, nil, nil)
	t.Cleanup(func() { store.Init(nil, nil, nil) })
	h := &notificationAPIHarness{t: t, db: db.DB, router: mux.NewRouter(), validationStatus: 200}
	// Only the remote gateway boundary is simulated; rule persistence,
	// ownership checks, versioning and event creation run real Core code.
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LazyMind-Internal-Token") != "fixture-internal-token" {
			w.WriteHeader(401)
			return
		}
		h.validationMu.Lock()
		h.validations++
		status := h.validationStatus
		h.validationMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != 200 {
			_, _ = w.Write([]byte(`{"error":{"code":"ACCOUNT_UNAVAILABLE","message":"账号不可用","retryable":false,"request_id":"fixture"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"valid":true,"provider":"feishu","targets":[{"account_id":"account-a","recipient_id":"group-a","valid":true},{"account_id":"account-b","recipient_id":"group-b","valid":true}]}`))
	}))
	t.Cleanup(gateway.Close)
	t.Setenv("LAZYMIND_CHANNEL_GATEWAY_URL", gateway.URL)
	t.Setenv("LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN", "fixture-internal-token")
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"passed":true}`))
	}))
	t.Cleanup(chat.Close)
	t.Setenv("LAZYMIND_CHAT_SERVICE_URL", chat.URL)
	registerCoreRoutes(h.router)
	return h
}

func enabledNotificationRule() notificationRule {
	return notificationRule{
		Events: map[string]notificationEventRule{
			"succeeded": {true, "summary"}, "failed": {true, "full"}, "paused": {true, "summary"},
		},
		Channels: []notificationChannelRule{{Provider: "feishu", Enabled: true, Targets: []notificationTarget{
			{"account-a", "group-a"}, {"account-b", "group-b"},
		}}},
	}
}

func (h *notificationAPIHarness) request(method, path, owner string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Request-Id", "notification-contract-request")
	if owner != "" {
		r.Header.Set("X-User-Id", owner)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, r)
	return w
}

func decodeNotificationResponse[T any](t *testing.T, w *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	raw := w.Body.Bytes()
	if data, ok := envelope["data"]; ok {
		raw = data
	}
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertNotificationError(t *testing.T, w *httptest.ResponseRecorder, status, code int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("missing structured error: %v", err)
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		payload = nested
	}
	if payload["code"] != float64(code) {
		t.Errorf("code=%v, want %d", payload["code"], code)
	}
	requestID := payload["request_id"]
	if data, ok := payload["data"].(map[string]any); ok && requestID == nil {
		requestID = data["request_id"]
	}
	if requestID != "notification-contract-request" {
		t.Errorf("missing request_id: %v", payload)
	}
	for _, secret := range []string{"fixture-internal-token", "SELECT ", "credentials_ciphertext", "Traceback", "/var/lib/"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("unsafe error: %s", secret)
		}
	}
}

func (h *notificationAPIHarness) settings(owner string) notificationSettings {
	return decodeNotificationResponse[notificationSettings](h.t, h.request("GET", "/user/notification-settings", owner, nil), 200)
}
func (h *notificationAPIHarness) schedule(owner string) orm.UserSchedule {
	h.t.Helper()
	s := orm.UserSchedule{UserID: owner, Name: "行业日报", CronExpr: "0 9 * * *", Timezone: "Asia/Shanghai", PromptTemplate: "整理行业日报", Enabled: true, KbIDs: "[]", FileIDs: "[]"}
	if err := scheduler.CreateSchedule(h.t.Context(), h.db, &s); err != nil {
		h.t.Fatal(err)
	}
	return s
}
func (h *notificationAPIHarness) rule(owner, id string) notificationRuleView {
	return decodeNotificationResponse[notificationRuleView](h.t, h.request("GET", "/schedules/"+id+"/notification-rule", owner, nil), 200)
}
func (h *notificationAPIHarness) saveRule(owner, id string, view notificationRuleView) notificationRuleView {
	return decodeNotificationResponse[notificationRuleView](h.t, h.request("PUT", "/schedules/"+id+"/notification-rule", owner, view), 200)
}
func (h *notificationAPIHarness) run(s orm.UserSchedule, status string) orm.TaskCenterTask {
	h.t.Helper()
	task := orm.TaskCenterTask{UserID: s.UserID, ScheduleID: &s.ID, ConversationID: fmt.Sprintf("conv-%d", time.Now().UnixNano()), TaskType: "scheduled", Status: status, Title: &s.Name, DefinitionVersion: s.DefinitionVersion}
	if err := taskcenter.CreateTask(h.t.Context(), h.db, &task); err != nil {
		h.t.Fatal(err)
	}
	return task
}
func (h *notificationAPIHarness) history(owner, id string) notificationHistory {
	return decodeNotificationResponse[notificationHistory](h.t, h.request("GET", "/task-center/tasks/"+id+"/notifications", owner, nil), 200)
}

func TestNotificationDefaultsAreSafeAndPersisted(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.settings("owner")
	if !s.Enabled || s.Version < 1 {
		t.Fatalf("invalid defaults: %+v", s)
	}
	for _, event := range []string{"succeeded", "failed", "paused"} {
		got, ok := s.Defaults.Events[event]
		if !ok || got.ContentMode != "summary" || got.Enabled != (event != "paused") {
			t.Errorf("default %s=%+v", event, got)
		}
	}
	for _, channel := range s.Defaults.Channels {
		if channel.Provider != "desktop" && channel.Enabled {
			t.Errorf("external channel auto-enabled: %+v", channel)
		}
	}
	s.Defaults = enabledNotificationRule()
	saved := decodeNotificationResponse[notificationSettings](t, h.request("PUT", "/user/notification-settings", "owner", s), 200)
	if saved.Version <= s.Version {
		t.Fatal("version did not advance")
	}
	// A fresh router must read durable data, not process-local defaults.
	h.router = mux.NewRouter()
	registerCoreRoutes(h.router)
	if got := h.settings("owner"); !reflect.DeepEqual(got, saved) {
		t.Fatalf("settings not durable: %+v", got)
	}
	if other := h.settings("other"); reflect.DeepEqual(other.Defaults, saved.Defaults) {
		t.Fatal("settings leaked across owners")
	}
}

func TestNotificationDefaultsOnlyApplyToNewSchedulesAndReset(t *testing.T) {
	h := newNotificationAPIHarness(t)
	old := h.schedule("owner")
	oldRule := h.rule("owner", old.ID)
	s := h.settings("owner")
	s.Defaults = enabledNotificationRule()
	decodeNotificationResponse[notificationSettings](t, h.request("PUT", "/user/notification-settings", "owner", s), 200)
	fresh := h.schedule("owner")
	if got := h.rule("owner", fresh.ID); !reflect.DeepEqual(got.Rule, s.Defaults) {
		t.Fatal("new schedule did not inherit defaults")
	}
	if got := h.rule("owner", old.ID); !reflect.DeepEqual(got, oldRule) {
		t.Fatal("global change rewrote old schedule")
	}
	reset := decodeNotificationResponse[notificationRuleView](t, h.request("POST", "/schedules/"+old.ID+"/notification-rule:reset", "owner", map[string]any{"version": oldRule.Version}), 200)
	if !reflect.DeepEqual(reset.Rule, s.Defaults) || reset.Version <= oldRule.Version {
		t.Fatalf("reset=%+v", reset)
	}
}

func TestNotificationRuleRetainsDisabledSelectionsAndMultipleTargets(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	view := h.rule("owner", s.ID)
	view.Rule = enabledNotificationRule()
	saved := h.saveRule("owner", s.ID, view)
	if !reflect.DeepEqual(saved.Rule, view.Rule) {
		t.Fatal("state-specific modes or destinations were collapsed")
	}
	saved.Rule.Events["failed"] = notificationEventRule{false, "full"}
	saved.Rule.Channels[0].Enabled = false
	disabled := h.saveRule("owner", s.ID, saved)
	if !reflect.DeepEqual(disabled.Rule, saved.Rule) {
		t.Fatal("disabled channel/event lost choices")
	}
	for event, rule := range disabled.Rule.Events {
		rule.Enabled = false
		disabled.Rule.Events[event] = rule
	}
	h.saveRule("owner", s.ID, disabled) // zero channels and zero events is legal for a task
}

func TestNotificationRejectsInvalidRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*notificationRule)
	}{
		{"missing-events", func(r *notificationRule) { r.Events = nil }},
		{"unknown-event", func(r *notificationRule) { r.Events["canceled"] = notificationEventRule{true, "full"} }},
		{"bad-content-mode", func(r *notificationRule) { r.Events["failed"] = notificationEventRule{true, "raw-secret"} }},
		{"enabled-channel-without-event", func(r *notificationRule) {
			for k, v := range r.Events {
				v.Enabled = false
				r.Events[k] = v
			}
		}},
		{"enabled-channel-without-target", func(r *notificationRule) { r.Channels[0].Targets = nil }},
		{"empty-account", func(r *notificationRule) { r.Channels[0].Targets[0].AccountID = "" }},
		{"empty-recipient", func(r *notificationRule) { r.Channels[0].Targets[0].RecipientID = "" }},
		{"duplicate-target", func(r *notificationRule) {
			r.Channels[0].Targets = append(r.Channels[0].Targets, r.Channels[0].Targets[0])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newNotificationAPIHarness(t)
			s := h.schedule("owner")
			before := h.rule("owner", s.ID)
			bad := before
			bad.Rule = enabledNotificationRule()
			tc.mutate(&bad.Rule)
			assertNotificationError(t, h.request("PUT", "/schedules/"+s.ID+"/notification-rule", "owner", bad), 400, 2002701)
			if got := h.rule("owner", s.ID); !reflect.DeepEqual(got, before) {
				t.Fatal("rejected rule changed stored data")
			}
		})
	}
}

func TestNotificationGlobalDefaultsMustKeepOneEvent(t *testing.T) {
	h := newNotificationAPIHarness(t)
	before := h.settings("owner")
	bad := h.settings("owner")
	for k, v := range bad.Defaults.Events {
		v.Enabled = false
		bad.Defaults.Events[k] = v
	}
	assertNotificationError(t, h.request("PUT", "/user/notification-settings", "owner", bad), 400, 2002701)
	if got := h.settings("owner"); !reflect.DeepEqual(got, before) {
		t.Fatal("invalid defaults persisted")
	}
}

func TestNotificationConcurrentRuleWritersHaveOneWinner(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	view := h.rule("owner", s.ID)
	view.Rule = enabledNotificationRule()
	start := make(chan struct{})
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- h.request("PUT", "/schedules/"+s.ID+"/notification-rule", "owner", view).Code
		}()
	}
	close(start)
	a, b := <-results, <-results
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("concurrent writes=%d,%d, want one success and one conflict", a, b)
	}
	assertNotificationError(t, h.request("PUT", "/schedules/"+s.ID+"/notification-rule", "owner", view), 409, 2002702)
}

func TestNotificationGatewayValidationFailsClosed(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	before := h.rule("owner", s.ID)
	view := before
	view.Rule = enabledNotificationRule()
	h.validationStatus = 503
	assertNotificationError(t, h.request("PUT", "/schedules/"+s.ID+"/notification-rule", "owner", view), 503, 2002710)
	if h.validations == 0 {
		t.Fatal("save bypassed gateway validation")
	}
	if got := h.rule("owner", s.ID); !reflect.DeepEqual(got, before) {
		t.Fatal("unvalidated targets persisted")
	}
}

func TestNotificationUnsupportedProviderDoesNotBecomeSendable(t *testing.T) {
	for _, provider := range []string{"wechat", "wecom", "unknown"} {
		t.Run(provider, func(t *testing.T) {
			h := newNotificationAPIHarness(t)
			s := h.schedule("owner")
			view := h.rule("owner", s.ID)
			view.Rule = enabledNotificationRule()
			view.Rule.Channels[0].Provider = provider
			assertNotificationError(t, h.request("PUT", "/schedules/"+s.ID+"/notification-rule", "owner", view), 400, 2002705)
		})
	}
}

func TestNotificationForeignScheduleAndTaskAreHidden(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	task := h.run(s, "running")
	for _, entry := range []struct{ method, path string }{
		{"GET", "/schedules/" + s.ID + "/notification-rule"},
		{"PUT", "/schedules/" + s.ID + "/notification-rule"},
		{"POST", "/schedules/" + s.ID + "/notification-rule:reset"},
		{"GET", "/task-center/tasks/" + task.ID + "/notifications"},
	} {
		t.Run(entry.method+entry.path, func(t *testing.T) {
			w := h.request(entry.method, entry.path, "intruder", notificationRuleView{1, enabledNotificationRule()})
			if w.Code != 404 {
				t.Fatalf("cross-user access=%d: %s", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal("missing route is not an ownership check")
			}
		})
	}
}

func TestNotificationPauseEpisodesAndExecutionSnapshot(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	view := h.rule("owner", s.ID)
	view.Rule = enabledNotificationRule()
	view = h.saveRule("owner", s.ID, view)
	task := h.run(s, "running")
	changed := view
	changed.Rule = enabledNotificationRule()
	changed.Rule.Channels[0].Targets = []notificationTarget{{"account-b", "group-b"}}
	h.saveRule("owner", s.ID, changed)
	for _, status := range []string{"waiting", "waiting", "running", "waiting"} {
		if err := taskcenter.UpdateTaskStatus(t.Context(), h.db, task.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	history := h.history("owner", task.ID)
	if len(history.Items) != 2 {
		t.Fatalf("pause episode count=%d, want 2", len(history.Items))
	}
	seen := map[string]bool{}
	for _, item := range history.Items {
		if item.Event != "paused" || item.RuleVersion != view.Version || len(item.Targets) != 2 {
			t.Errorf("incorrect snapshot/event: %+v", item)
		}
		if item.ID == "" || seen[item.ID] {
			t.Fatal("pause instances share an event identity")
		}
		seen[item.ID] = true
	}
}

func TestNotificationNonEventsDoNotNotify(t *testing.T) {
	for _, state := range []string{"waiting_inputs", "canceled", "skipped"} {
		t.Run(state, func(t *testing.T) {
			h := newNotificationAPIHarness(t)
			s := h.schedule("owner")
			v := h.rule("owner", s.ID)
			v.Rule = enabledNotificationRule()
			h.saveRule("owner", s.ID, v)
			task := h.run(s, "running")
			if err := taskcenter.UpdateTaskStatus(t.Context(), h.db, task.ID, state); err != nil {
				t.Fatal(err)
			}
			if got := h.history("owner", task.ID); len(got.Items) != 0 {
				t.Fatalf("%s generated notifications", state)
			}
		})
	}
}

func TestNotificationTransactionRollbackDoesNotPublishEvent(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	v := h.rule("owner", s.ID)
	v.Rule = enabledNotificationRule()
	h.saveRule("owner", s.ID, v)
	task := h.run(s, "running")
	sentinel := fmt.Errorf("fixture transaction rollback")
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if e := taskcenter.UpdateTaskFailure(context.Background(), tx, task.ID, "执行失败"); e != nil {
			return e
		}
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("transaction=%v", err)
	}
	var saved orm.TaskCenterTask
	if err := h.db.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != "running" || len(h.history("owner", task.ID).Items) != 0 {
		t.Fatal("rollback leaked status/event")
	}
}

func TestNotificationDisableImpactRequiresFreshConfirmation(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	v := h.rule("owner", s.ID)
	v.Rule = enabledNotificationRule()
	h.saveRule("owner", s.ID, v)
	first := h.run(s, "running")
	impact := decodeNotificationResponse[map[string]any](t, h.request("GET", "/user/notification-settings/disable-impact", "owner", nil), 200)
	encoded, _ := json.Marshal(impact)
	if !bytes.Contains(encoded, []byte(first.ID)) || impact["impact_revision"] == nil {
		t.Fatal("impact omits active task or revision")
	}
	h.run(s, "running")
	saved := h.settings("owner")
	assertNotificationError(t, h.request("PUT", "/user/notification-settings", "owner", map[string]any{"enabled": false, "version": saved.Version, "defaults": saved.Defaults, "impact_revision": impact["impact_revision"]}), 409, 2002706)
	if !h.settings("owner").Enabled {
		t.Fatal("stale confirmation disabled notifications")
	}
}

func TestNotificationInternalAuthorizationRejectsMissingCredentials(t *testing.T) {
	h := newNotificationAPIHarness(t)
	for _, token := range []string{"", "invalid"} {
		t.Run(token, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/internal/task-notifications/not-owned:authorize", strings.NewReader(`{"part_id":"part-1"}`))
			r.Header.Set("X-User-Id", "owner")
			r.Header.Set("X-LazyMind-Internal-Token", token)
			w := httptest.NewRecorder()
			h.router.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("internal route accepted/missing, HTTP %d", w.Code)
			}
		})
	}
}

func TestNotificationRoutesHaveOpenAPIContracts(t *testing.T) {
	router := mux.NewRouter()
	registerCoreRoutes(router)
	raw, err := buildOpenAPISpecFromRouter(router)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for path, method := range map[string]string{
		"/user/notification-settings": "put", "/user/notification-settings/disable-impact": "get",
		"/schedules/{schedule_id}/notification-rule": "put", "/schedules/{schedule_id}/notification-rule:reset": "post",
		"/task-center/tasks/{task_id}/notifications": "get", "/task-center/notifications/{notification_id}:retry": "post",
	} {
		if len(spec.Paths[apiPrefix+path][method]) == 0 {
			t.Errorf("missing OpenAPI operation %s %s", method, path)
		}
	}
}

func TestNotificationLegacyEditDoesNotClearRule(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	v := h.rule("owner", s.ID)
	v.Rule = enabledNotificationRule()
	before := h.saveRule("owner", s.ID, v)
	decodeNotificationResponse[map[string]any](t, h.request("PUT", "/schedules/"+s.ID, "owner", map[string]any{"name": "修改标题"}), 200)
	if got := h.rule("owner", s.ID); !reflect.DeepEqual(got, before) {
		t.Fatal("legacy schedule edit cleared notification rule")
	}
}

func TestNotificationCreateAndBatchPersistExplicitRules(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			h := newNotificationAPIHarness(t)
			body := map[string]any{"name": "日报", "cron_expr": "0 9 * * *", "timezone": "UTC", "prompt_template": "测试日报", "notification_rule": enabledNotificationRule()}
			var ids []string
			if batch {
				body["client_key"] = "first"
				payload := map[string]any{"group": map[string]any{"name": "日报组", "timezone": "UTC"}, "tasks": []any{body}}
				result := decodeNotificationResponse[struct {
					ScheduleIDs map[string]string `json:"schedule_ids"`
				}](t, h.request("POST", "/automation-groups:batch-create", "owner", payload), 200)
				for _, id := range result.ScheduleIDs {
					ids = append(ids, id)
				}
			} else {
				result := decodeNotificationResponse[map[string]any](t, h.request("POST", "/schedules", "owner", body), 200)
				ids = append(ids, result["id"].(string))
			}
			if len(ids) != 1 {
				t.Fatalf("created schedules=%v", ids)
			}
			if got := h.rule("owner", ids[0]); !reflect.DeepEqual(got.Rule, enabledNotificationRule()) {
				t.Fatal("creation ignored explicit notification rule")
			}
		})
	}
}

func TestNotificationFailureIsDeduplicatedAndSafe(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	v := h.rule("owner", s.ID)
	v.Rule = enabledNotificationRule()
	h.saveRule("owner", s.ID, v)
	task := h.run(s, "running")
	for i := 0; i < 2; i++ {
		if err := taskcenter.UpdateTaskFailure(t.Context(), h.db, task.ID, "SELECT credentials_ciphertext FROM accounts; fixture-internal-token /var/lib/private"); err != nil {
			t.Fatal(err)
		}
	}
	history := h.history("owner", task.ID)
	if len(history.Items) != 1 || history.Items[0].Event != "failed" {
		t.Fatalf("failure events=%+v", history.Items)
	}
	w := h.request("GET", "/task-center/tasks/"+task.ID+"/notifications", "owner", nil)
	for _, secret := range []string{"SELECT ", "credentials_ciphertext", "fixture-internal-token", "/var/lib/private"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("failure history leaked %s", secret)
		}
	}
}

func TestNotificationDisabledEventDoesNotCreateHistory(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	v := h.rule("owner", s.ID)
	v.Rule = enabledNotificationRule()
	v.Rule.Events["paused"] = notificationEventRule{false, "full"}
	h.saveRule("owner", s.ID, v)
	task := h.run(s, "running")
	if err := taskcenter.UpdateTaskStatus(t.Context(), h.db, task.ID, "waiting"); err != nil {
		t.Fatal(err)
	}
	if len(h.history("owner", task.ID).Items) != 0 {
		t.Fatal("disabled pause event emitted")
	}
}

func TestNotificationEventInsertFailureRollsBackTaskStatus(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.schedule("owner")
	v := h.rule("owner", s.ID)
	v.Rule = enabledNotificationRule()
	h.saveRule("owner", s.ID, v)
	task := h.run(s, "running")
	name := "notification_fixture_event_write_failure"
	if err := h.db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_notification_events" {
			tx.AddError(fmt.Errorf("fixture database write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Create().Remove(name) })
	if err := taskcenter.UpdateTaskFailure(t.Context(), h.db, task.ID, "执行失败"); err == nil {
		t.Fatal("event write failure was ignored")
	}
	var saved orm.TaskCenterTask
	if err := h.db.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != "running" || len(h.history("owner", task.ID).Items) != 0 {
		t.Fatal("task/event write was not atomic")
	}
}

func TestNotificationGlobalDisableConfirmAndReenableRetainConfiguration(t *testing.T) {
	h := newNotificationAPIHarness(t)
	s := h.settings("owner")
	s.Defaults = enabledNotificationRule()
	s = decodeNotificationResponse[notificationSettings](t, h.request("PUT", "/user/notification-settings", "owner", s), 200)
	before := s.Defaults
	impact := decodeNotificationResponse[map[string]any](t, h.request("GET", "/user/notification-settings/disable-impact", "owner", nil), 200)
	s = decodeNotificationResponse[notificationSettings](t, h.request("PUT", "/user/notification-settings", "owner", map[string]any{"enabled": false, "version": s.Version, "defaults": s.Defaults, "impact_revision": impact["impact_revision"]}), 200)
	if s.Enabled || !reflect.DeepEqual(s.Defaults, before) {
		t.Fatal("disable changed saved choices")
	}
	s.Enabled = true
	s = decodeNotificationResponse[notificationSettings](t, h.request("PUT", "/user/notification-settings", "owner", s), 200)
	if !s.Enabled || !reflect.DeepEqual(s.Defaults, before) {
		t.Fatal("re-enable lost saved choices")
	}
}

func TestNotificationAnonymousSettingsCannotBecomeDefaultUserSettings(t *testing.T) {
	h := newNotificationAPIHarness(t)
	for _, method := range []string{"GET", "PUT"} {
		w := h.request(method, "/user/notification-settings", "", notificationSettings{true, 1, enabledNotificationRule()})
		if w.Code != 400 && w.Code != 401 {
			t.Fatalf("anonymous notification settings returned %d", w.Code)
		}
	}
}
