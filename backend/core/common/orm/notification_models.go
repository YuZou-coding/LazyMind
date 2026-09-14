package orm

import "time"

// NotificationSettings is the owner's switch and defaults. Generation changes
// only when disabling, permanently fencing work accepted before that change.
type NotificationSettings struct {
	UserID       string    `gorm:"primaryKey;size:256"`
	Enabled      bool      `gorm:"not null"`
	Version      int       `gorm:"not null"`
	Generation   int       `gorm:"not null"`
	DefaultsJSON string    `gorm:"type:text;not null"`
	UpdatedAt    time.Time `gorm:"not null"`
}

func (NotificationSettings) TableName() string { return "notification_settings" }

type ScheduleNotificationRule struct {
	ScheduleID string    `gorm:"primaryKey;size:256"`
	UserID     string    `gorm:"size:256;not null;index:idx_notification_rules_owner"`
	Version    int       `gorm:"not null"`
	RuleJSON   string    `gorm:"type:text;not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

// A snapshot belongs to an execution, including executions waiting for inputs.
type TaskNotificationSnapshot struct {
	TaskID      string `gorm:"primaryKey;size:256"`
	UserID      string `gorm:"size:256;not null"`
	RuleVersion int    `gorm:"not null"`
	RuleJSON    string `gorm:"type:text;not null"`
	Episode     int    `gorm:"not null"`
}

type TaskNotificationEvent struct {
	ID            string     `gorm:"primaryKey;size:256" json:"id"`
	TaskID        string     `gorm:"size:256;not null;uniqueIndex:idx_notification_event_episode,priority:1" json:"task_id"`
	UserID        string     `gorm:"size:256;not null;index:idx_notification_events_owner" json:"-"`
	Event         string     `gorm:"size:32;not null" json:"event"`
	Episode       int        `gorm:"not null;uniqueIndex:idx_notification_event_episode,priority:2" json:"-"`
	RuleVersion   int        `gorm:"not null" json:"rule_version"`
	Generation    int        `gorm:"not null" json:"-"`
	Status        string     `gorm:"size:32;not null;index:idx_notification_events_work" json:"status"`
	Revision      int        `gorm:"not null" json:"revision"`
	ContentJSON   string     `gorm:"type:text;not null" json:"-"`
	SummaryStatus string     `gorm:"size:32;not null" json:"summary_status"`
	LeaseOwner    string     `gorm:"size:256;not null" json:"-"`
	LeaseUntil    *time.Time `json:"-"`
	NextAttemptAt time.Time  `gorm:"not null" json:"-"`
	CreatedAt     time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"not null" json:"updated_at"`
}

type TaskNotificationDelivery struct {
	ID          string    `gorm:"primaryKey;size:256" json:"id"`
	EventID     string    `gorm:"size:256;not null;uniqueIndex:idx_notification_delivery_target,priority:1" json:"-"`
	Provider    string    `gorm:"size:32;not null" json:"provider"`
	AccountID   string    `gorm:"size:256;not null;uniqueIndex:idx_notification_delivery_target,priority:2" json:"account_id"`
	RecipientID string    `gorm:"size:256;not null;uniqueIndex:idx_notification_delivery_target,priority:3" json:"recipient_id"`
	ContentMode string    `gorm:"size:32;not null" json:"content_mode"`
	Status      string    `gorm:"size:32;not null" json:"status"`
	Revision    int       `gorm:"not null" json:"revision"`
	PayloadJSON string    `gorm:"type:text;not null" json:"-"`
	PartsJSON   string    `gorm:"type:text;not null" json:"-"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}
