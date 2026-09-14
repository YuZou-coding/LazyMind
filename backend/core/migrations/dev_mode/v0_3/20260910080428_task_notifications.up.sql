-- Task notification state starts empty; legacy tasks never enqueue on upgrade.
-- +migrate Dialect postgres
CREATE TABLE notification_settings (
 user_id VARCHAR(256) PRIMARY KEY, enabled BOOLEAN NOT NULL, version INTEGER NOT NULL,
 generation INTEGER NOT NULL, defaults_json TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE schedule_notification_rules (
 schedule_id VARCHAR(256) PRIMARY KEY, user_id VARCHAR(256) NOT NULL,
 version INTEGER NOT NULL, rule_json TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_notification_rules_owner ON schedule_notification_rules(user_id);
CREATE TABLE task_notification_snapshots (
 task_id VARCHAR(256) PRIMARY KEY, user_id VARCHAR(256) NOT NULL, rule_version INTEGER NOT NULL,
 rule_json TEXT NOT NULL, episode INTEGER NOT NULL
);
CREATE TABLE task_notification_events (
 id VARCHAR(256) PRIMARY KEY, task_id VARCHAR(256) NOT NULL, user_id VARCHAR(256) NOT NULL,
 event VARCHAR(32) NOT NULL, episode INTEGER NOT NULL, rule_version INTEGER NOT NULL,
 generation INTEGER NOT NULL, status VARCHAR(32) NOT NULL, revision INTEGER NOT NULL,
 content_json TEXT NOT NULL, summary_status VARCHAR(32) NOT NULL,
 lease_owner VARCHAR(256) NOT NULL, lease_until TIMESTAMPTZ, next_attempt_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_notification_event_episode ON task_notification_events(task_id,episode);
CREATE INDEX idx_notification_events_owner ON task_notification_events(user_id);
CREATE INDEX idx_notification_events_work ON task_notification_events(status);
CREATE TABLE task_notification_deliveries (
 id VARCHAR(256) PRIMARY KEY, event_id VARCHAR(256) NOT NULL, provider VARCHAR(32) NOT NULL,
 account_id VARCHAR(256) NOT NULL, recipient_id VARCHAR(256) NOT NULL, content_mode VARCHAR(32) NOT NULL,
 status VARCHAR(32) NOT NULL, revision INTEGER NOT NULL, payload_json TEXT NOT NULL,
 parts_json TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_notification_delivery_target ON task_notification_deliveries(event_id,account_id,recipient_id);

-- +migrate Dialect sqlite
CREATE TABLE notification_settings (
 user_id VARCHAR(256) PRIMARY KEY, enabled BOOLEAN NOT NULL, version INTEGER NOT NULL,
 generation INTEGER NOT NULL, defaults_json TEXT NOT NULL, updated_at DATETIME NOT NULL
);
CREATE TABLE schedule_notification_rules (
 schedule_id VARCHAR(256) PRIMARY KEY, user_id VARCHAR(256) NOT NULL,
 version INTEGER NOT NULL, rule_json TEXT NOT NULL, updated_at DATETIME NOT NULL
);
CREATE INDEX idx_notification_rules_owner ON schedule_notification_rules(user_id);
CREATE TABLE task_notification_snapshots (
 task_id VARCHAR(256) PRIMARY KEY, user_id VARCHAR(256) NOT NULL, rule_version INTEGER NOT NULL,
 rule_json TEXT NOT NULL, episode INTEGER NOT NULL
);
CREATE TABLE task_notification_events (
 id VARCHAR(256) PRIMARY KEY, task_id VARCHAR(256) NOT NULL, user_id VARCHAR(256) NOT NULL,
 event VARCHAR(32) NOT NULL, episode INTEGER NOT NULL, rule_version INTEGER NOT NULL,
 generation INTEGER NOT NULL, status VARCHAR(32) NOT NULL, revision INTEGER NOT NULL,
 content_json TEXT NOT NULL, summary_status VARCHAR(32) NOT NULL,
 lease_owner VARCHAR(256) NOT NULL, lease_until DATETIME, next_attempt_at DATETIME NOT NULL,
 created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX idx_notification_event_episode ON task_notification_events(task_id,episode);
CREATE INDEX idx_notification_events_owner ON task_notification_events(user_id);
CREATE INDEX idx_notification_events_work ON task_notification_events(status);
CREATE TABLE task_notification_deliveries (
 id VARCHAR(256) PRIMARY KEY, event_id VARCHAR(256) NOT NULL, provider VARCHAR(32) NOT NULL,
 account_id VARCHAR(256) NOT NULL, recipient_id VARCHAR(256) NOT NULL, content_mode VARCHAR(32) NOT NULL,
 status VARCHAR(32) NOT NULL, revision INTEGER NOT NULL, payload_json TEXT NOT NULL,
 parts_json TEXT NOT NULL, updated_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX idx_notification_delivery_target ON task_notification_deliveries(event_id,account_id,recipient_id);
