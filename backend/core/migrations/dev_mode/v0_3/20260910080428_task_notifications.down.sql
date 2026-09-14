-- +migrate Dialect postgres
DROP TABLE task_notification_deliveries;
DROP TABLE task_notification_events;
DROP TABLE task_notification_snapshots;
DROP TABLE schedule_notification_rules;
DROP TABLE notification_settings;

-- +migrate Dialect sqlite
DROP TABLE task_notification_deliveries;
DROP TABLE task_notification_events;
DROP TABLE task_notification_snapshots;
DROP TABLE schedule_notification_rules;
DROP TABLE notification_settings;
