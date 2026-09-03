package common

import (
	"net/http"
	"testing"
)

func TestLocalWorkspaceErrorsHaveStableDedicatedCodes(t *testing.T) {
	tests := []struct {
		semantic string
		key      string
		status   int
		code     int
	}{
		{"LOCAL_WORKSPACE_MODE_FORBIDDEN", "local workspace mode forbidden", http.StatusForbidden, 2002321},
		{"LOCAL_WORKSPACE_SELECTION_FORBIDDEN", "local workspace selection forbidden", http.StatusForbidden, 2002322},
		{"LOCAL_WORKSPACE_SELECTION_EXPIRED", "local workspace selection expired", http.StatusGone, 2002323},
		{"LOCAL_WORKSPACE_SELECTION_INVALID", "local workspace selection invalid", http.StatusBadRequest, 2002324},
		{"LOCAL_WORKSPACE_PATH_INVALID", "local workspace path invalid", http.StatusBadRequest, 2002325},
		{"LOCAL_WORKSPACE_NOT_FOUND", "local workspace not found", http.StatusNotFound, 2002326},
		{"LOCAL_WORKSPACE_REVOKED", "local workspace revoked", http.StatusConflict, 2002327},
		{"LOCAL_WORKSPACE_BINDING_LOCKED", "local workspace binding locked", http.StatusConflict, 2002328},
		{"LOCAL_WORKSPACE_BINDING_CONFLICT", "local workspace binding conflict", http.StatusConflict, 2002329},
		{"LOCAL_WORKSPACE_PATH_UNAVAILABLE", "local workspace path unavailable", http.StatusConflict, 2002330},
		{"LOCAL_FILE_ACCESS_NOT_ENABLED", "local file access not enabled", http.StatusNotImplemented, 2002331},
	}

	for _, test := range tests {
		t.Run(test.semantic, func(t *testing.T) {
			resolved, exists := lookupErrorCatalog(test.key)
			if !exists {
				t.Fatalf("error catalog entry %q is missing", test.key)
			}
			if resolved.HTTPStatus != test.status || resolved.Code != test.code {
				t.Fatalf(
					"%s resolved to status/code %d/%d, want %d/%d",
					test.semantic,
					resolved.HTTPStatus,
					resolved.Code,
					test.status,
					test.code,
				)
			}
		})
	}
}
