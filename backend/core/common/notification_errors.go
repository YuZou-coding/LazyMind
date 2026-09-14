package common

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
)

// Backend-owned source; publishing the same entries into UI translations is
// a separate integration step when directory scope permits it.
//
//go:embed notification_error_translations.json
var notificationErrorTranslationsJSON []byte

func NotificationErrorMessage(code int, language, fallback string) string {
	translations := map[string]map[string]string{}
	if json.Unmarshal(notificationErrorTranslationsJSON, &translations) != nil {
		return fallback
	}
	locale := "zh-CN"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en") {
		locale = "en-US"
	}
	if message := translations[strconv.Itoa(code)][locale]; message != "" {
		return message
	}
	return fallback
}

const (
	ErrCodeNotificationInvalid     = 2002701
	ErrCodeNotificationConflict    = 2002702
	ErrCodeNotificationUnsupported = 2002705
	ErrCodeNotificationImpact      = 2002706
	ErrCodeNotificationDuplicate   = 2002707
	ErrCodeNotificationUnavailable = 2002710
)
