package main

func nullableStrSchema() map[string]any {
	return map[string]any{"type": "string", "nullable": true}
}

// These source definitions accompany the registered, centrally authorized
// routes. Responses describe the existing schedule API's plain JSON format.
func notificationOpenAPISchemas(s map[string]any) map[string]any {
	version := map[string]any{"type": "integer", "minimum": 1}
	id := map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
	s["NotificationEventRule"] = objReq([]string{"enabled", "content_mode"},
		prop("enabled", boolSchema()), prop("content_mode", enumStringSchema("summary", "full")))
	s["NotificationTarget"] = objReq([]string{"account_id", "recipient_id"}, prop("account_id", id), prop("recipient_id", id))
	s["NotificationChannelRule"] = objReq([]string{"provider", "enabled", "targets"},
		prop("provider", enumStringSchema("feishu")), prop("enabled", boolSchema()),
		prop("targets", map[string]any{"type": "array", "items": refSchema("NotificationTarget"), "maxItems": 100, "uniqueItems": true}))
	s["NotificationRule"] = objReq([]string{"events", "channels"},
		prop("events", objReq([]string{"succeeded", "failed", "paused"},
			prop("succeeded", refSchema("NotificationEventRule")), prop("failed", refSchema("NotificationEventRule")), prop("paused", refSchema("NotificationEventRule")))),
		prop("channels", map[string]any{"type": "array", "items": refSchema("NotificationChannelRule"), "maxItems": 8}))
	s["NotificationRule"].(map[string]any)["description"] = "Each event keeps its content choice. Enabled channels require targets and an enabled event. Disabled configuration is retained. Only Feishu is supported in this release."
	s["NotificationSettings"] = objReq([]string{"enabled", "version", "defaults"},
		prop("enabled", boolSchema()), prop("version", version), prop("defaults", refSchema("NotificationRule")))
	s["NotificationSettingsUpdate"] = objReq([]string{"enabled", "version", "defaults"},
		prop("enabled", boolSchema()), prop("version", version), prop("defaults", refSchema("NotificationRule")), prop("impact_revision", strSchema()))
	s["NotificationSettingsUpdate"].(map[string]any)["description"] = "Supply the current version; conflicts return 409. Disabling requires the latest disable-impact revision. Global defaults must retain an enabled event and apply only to newly created schedules."
	s["ScheduleNotificationRule"] = objReq([]string{"version", "rule"}, prop("version", version), prop("rule", refSchema("NotificationRule")))
	s["NotificationResetRequest"] = objReq([]string{"version"}, prop("version", version))
	s["NotificationImpactItem"] = objReq([]string{"task_id"}, prop("task_id", id), prop("status", strSchema()), prop("notification_id", id), prop("revision", version))
	s["NotificationDisableImpact"] = objReq([]string{"items", "impact_revision"},
		prop("items", array(refSchema("NotificationImpactItem"))), prop("impact_revision", strSchema()))
	s["NotificationPart"] = objReq([]string{"part_id", "kind", "status"},
		prop("part_id", strSchema()), prop("kind", enumStringSchema("card", "image", "file")),
		prop("artifact_id", nullableStrSchema()), prop("name", strSchema()),
		prop("status", enumStringSchema("pending", "sending", "sent", "failed", "unknown", "skipped")),
		prop("error_code", nullableStrSchema()), prop("attempt_count", intSchema()), prop("message_id", nullableStrSchema()), prop("sent_at", nullableStrSchema()))
	s["NotificationDelivery"] = objReq([]string{"id", "provider", "account_id", "recipient_id", "content_mode", "status", "revision", "parts", "updated_at"},
		prop("id", id), prop("provider", strSchema()), prop("account_id", id), prop("recipient_id", id),
		prop("content_mode", enumStringSchema("summary", "full")), prop("status", strSchema()), prop("revision", version),
		prop("parts", array(refSchema("NotificationPart"))), prop("updated_at", dateTimeSchema()))
	s["NotificationEvent"] = objReq([]string{"id", "task_id", "event", "rule_version", "status", "revision", "summary_status", "created_at", "updated_at", "targets"},
		prop("id", id), prop("task_id", id), prop("event", enumStringSchema("succeeded", "failed", "paused")), prop("rule_version", version),
		prop("status", enumStringSchema("pending", "queued", "retrying", "sent", "partial", "failed", "unknown", "canceled")),
		prop("revision", version), prop("summary_status", enumStringSchema("pending", "ready", "failed")),
		prop("created_at", dateTimeSchema()), prop("updated_at", dateTimeSchema()), prop("targets", array(refSchema("NotificationDelivery"))))
	s["NotificationHistory"] = objReq([]string{"items"}, prop("items", array(refSchema("NotificationEvent"))))
	s["NotificationRetryRequest"] = objReq([]string{"expected_revision"}, prop("expected_revision", version), prop("confirm_duplicate_risk", boolSchema()))
	s["NotificationRetryAccepted"] = objReq([]string{"id", "status"}, prop("id", id), prop("status", enumStringSchema("retrying")))
	s["NotificationError"] = objReq([]string{"code", "message", "request_id"}, prop("code", intSchema()), prop("message", strSchema()), prop("request_id", strSchema()))
	s["NotificationError"].(map[string]any)["description"] = "Stable codes: 2002701 invalid configuration; 2002702 version/state conflict; 2002705 unsupported channel; 2002706 stale disable impact; 2002707 duplicate-risk confirmation required; 2002710 dependency unavailable. Authentication and missing resources use common Core codes."
	for _, name := range []string{"NotificationEventRule", "NotificationTarget", "NotificationChannelRule", "NotificationRule", "NotificationSettingsUpdate", "ScheduleNotificationRule", "NotificationResetRequest", "NotificationRetryRequest"} {
		s[name].(map[string]any)["additionalProperties"] = false
	}
	s["NotificationScheduleDependency"] = obj(prop("source_schedule_id", id), prop("source_client_key", strSchema()),
		prop("window_type", strSchema()), prop("content_types", array(strSchema())), prop("incomplete_policy", strSchema()), prop("max_wait_seconds", intSchema()))
	schedule := objReq([]string{"cron_expr", "prompt_template"}, prop("name", strSchema()), prop("remark", strSchema()),
		prop("cron_expr", strSchema()), prop("timezone", strSchema()), prop("prompt_template", strSchema()),
		prop("kb_ids", array(strSchema())), prop("file_ids", array(strSchema())), prop("group_id", nullableStrSchema()),
		prop("dependencies", array(refSchema("NotificationScheduleDependency"))), prop("notification_rule", refSchema("NotificationRule")))
	schedule["description"] = "notification_rule is optional; omission copies current global defaults when creating a schedule. Existing edit requests never clear notification rules."
	s["NotificationScheduleCreate"] = schedule
	batchTask := objReq([]string{"client_key", "cron_expr", "prompt_template"})
	for name, property := range schedule["properties"].(map[string]any) {
		if name != "group_id" {
			batchTask["properties"].(map[string]any)[name] = property
		}
	}
	batchTask["properties"].(map[string]any)["client_key"] = strSchema()
	s["NotificationScheduleBatchTask"] = batchTask
	s["NotificationScheduleBatchCreate"] = objReq([]string{"group", "tasks"},
		prop("group", objReq([]string{"name"}, prop("name", strSchema()), prop("remark", strSchema()), prop("timezone", strSchema()))),
		prop("tasks", array(refSchema("NotificationScheduleBatchTask"))))
	return s
}

func notificationOpenAPIPaths(paths map[string]any) map[string]any {
	for _, endpoint := range []struct{ path, method, input, output, summary string }{
		{"/user/notification-settings", "get", "", "NotificationSettings", "Read notification switch and defaults"},
		{"/user/notification-settings", "put", "NotificationSettingsUpdate", "NotificationSettings", "Update notification switch and defaults"},
		{"/user/notification-settings/disable-impact", "get", "", "NotificationDisableImpact", "Inspect the current impact of disabling notifications"},
		{"/schedules/{schedule_id}/notification-rule", "get", "", "ScheduleNotificationRule", "Read the schedule's independent notification rule"},
		{"/schedules/{schedule_id}/notification-rule", "put", "ScheduleNotificationRule", "ScheduleNotificationRule", "Save the rule for the next execution"},
		{"/schedules/{schedule_id}/notification-rule:reset", "post", "NotificationResetRequest", "ScheduleNotificationRule", "Restore current global defaults"},
		{"/task-center/tasks/{task_id}/notifications", "get", "", "NotificationHistory", "List execution notifications and per-target parts"},
		{"/task-center/notifications/{notification_id}:retry", "post", "NotificationRetryRequest", "NotificationRetryAccepted", "Retry incomplete parts with original content and targets"},
	} {
		var body map[string]any
		if endpoint.input != "" {
			body = jsonBody(refSchema(endpoint.input), true)
		}
		operation := op(endpoint.summary, nil, body, response(200, "Success", refSchema(endpoint.output)))
		operation["tags"] = []string{"Task notifications"}
		responses := operation["responses"].(map[string]any)
		if endpoint.output == "NotificationRetryAccepted" {
			delete(responses, "200")
			responses["202"] = response(202, "Retry intent persisted; delivery is asynchronous", refSchema(endpoint.output))
		}
		for _, status := range []string{"400", "401", "403", "404", "409", "500", "503"} {
			responses[status] = map[string]any{"description": "Safe business error", "content": map[string]any{"application/json": map[string]any{"schema": refSchema("NotificationError")}}}
		}
		path, ok := paths[endpoint.path].(map[string]any)
		if !ok {
			path = map[string]any{}
			paths[endpoint.path] = path
		}
		path[endpoint.method] = operation
	}
	// Keep the legacy schedule response and annotate the added input field.
	for path, schema := range map[string]string{"/schedules": "NotificationScheduleCreate", "/automation-groups:batch-create": "NotificationScheduleBatchCreate"} {
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		operation, ok := item["post"].(map[string]any)
		if !ok {
			operation = op("Create schedules with optional notification rules", nil, nil, response(200, "Created schedule or batch identifiers", obj()))
			item["post"] = operation
		}
		operation["requestBody"] = jsonBody(refSchema(schema), true)
	}
	return paths
}
