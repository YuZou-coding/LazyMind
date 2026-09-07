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
		{"LOCAL_WORKSPACE_MODE_FORBIDDEN", "local workspace mode forbidden", http.StatusForbidden, 2002370},
		{"LOCAL_WORKSPACE_SELECTION_FORBIDDEN", "local workspace selection forbidden", http.StatusForbidden, 2002371},
		{"LOCAL_WORKSPACE_SELECTION_EXPIRED", "local workspace selection expired", http.StatusGone, 2002372},
		{"LOCAL_WORKSPACE_SELECTION_INVALID", "local workspace selection invalid", http.StatusBadRequest, 2002373},
		{"LOCAL_WORKSPACE_PATH_INVALID", "local workspace path invalid", http.StatusBadRequest, 2002374},
		{"LOCAL_WORKSPACE_NOT_FOUND", "local workspace not found", http.StatusNotFound, 2002375},
		{"LOCAL_WORKSPACE_REVOKED", "local workspace revoked", http.StatusConflict, 2002376},
		{"LOCAL_WORKSPACE_BINDING_LOCKED", "local workspace binding locked", http.StatusConflict, 2002377},
		{"LOCAL_WORKSPACE_BINDING_CONFLICT", "local workspace binding conflict", http.StatusConflict, 2002378},
		{"LOCAL_WORKSPACE_PATH_UNAVAILABLE", "local workspace path unavailable", http.StatusConflict, 2002379},
		{"LOCAL_FILE_ACCESS_NOT_ENABLED", "local file access not enabled", http.StatusNotImplemented, 2002380},
		{"SUBAGENT_WORKSPACE_RESOLUTION_FAILED", "resolve subagent workspace", http.StatusInternalServerError, 2002398},
		{"LOCAL_WORKSPACE_DIRECTORY_UNAVAILABLE", "workspace directory unavailable", http.StatusConflict, 2002399},
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
