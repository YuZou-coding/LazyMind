package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gorilla/mux"
)

func TestOpenAPILocalWorkspacePublicContract(t *testing.T) {
	router := mux.NewRouter()
	registerCoreRoutes(router)
	specJSON, err := buildOpenAPISpecFromRouter(router)
	if err != nil {
		t.Fatalf("build openapi spec: %v", err)
	}

	var spec map[string]any
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		t.Fatalf("decode openapi spec: %v", err)
	}

	tests := []struct {
		method      string
		path        string
		responseRef string
	}{
		{"get", "/api/core/local-workspaces", "#/components/schemas/LocalWorkspaceListResponse"},
		{"post", "/api/core/local-workspaces/{workspace_id}:revoke", "#/components/schemas/LocalWorkspaceRevokeResponse"},
		{"get", "/api/core/conversations/{conversation_id}:workspace", "#/components/schemas/ConversationLocalWorkspaceResponse"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			op := openAPIOperationForTest(t, spec, test.method, test.path)
			if got := openAPIObjectResponseRefForTest(t, op); got != test.responseRef {
				t.Fatalf("response ref = %q, want %q", got, test.responseRef)
			}
		})
	}

	paths := spec["paths"].(map[string]any)
	for _, privatePath := range []string{
		"/api/core/internal/local-workspaces",
		"/api/core/internal/local-workspaces:resolve",
	} {
		if _, exists := paths[privatePath]; exists {
			t.Fatalf("trusted-host workspace route leaked into public OpenAPI: %s", privatePath)
		}
	}
}

func TestOpenAPILocalWorkspaceSchemasDoNotAcceptClientPaths(t *testing.T) {
	router := mux.NewRouter()
	registerCoreRoutes(router)
	specJSON, err := buildOpenAPISpecFromRouter(router)
	if err != nil {
		t.Fatalf("build openapi spec: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		t.Fatalf("decode openapi spec: %v", err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)

	for _, schemaName := range []string{
		"LocalWorkspaceRevokeRequest",
		"ConversationLocalWorkspaceBindingRequest",
	} {
		properties := schemaPropertiesForTest(t, schemas, schemaName)
		for _, forbidden := range []string{"path", "canonical_path", "real_path"} {
			if _, exists := properties[forbidden]; exists {
				t.Fatalf("%s must not accept renderer supplied %q", schemaName, forbidden)
			}
		}
	}
	binding := schemaPropertiesForTest(t, schemas, "ConversationLocalWorkspaceBindingRequest")
	if _, exists := binding["workspace_id"]; !exists {
		t.Fatal("ConversationLocalWorkspaceBindingRequest.workspace_id missing")
	}

	workspace := schemaPropertiesForTest(t, schemas, "LocalWorkspace")
	for _, required := range []string{
		"workspace_id", "display_name", "path", "status", "version",
		"read_policy", "write_policy", "authorized_at", "last_used_at",
	} {
		if _, exists := workspace[required]; !exists {
			t.Fatalf("LocalWorkspace.%s missing", required)
		}
	}
	for _, forbidden := range []string{"canonical_path", "directory_identity", "file_content", "file_list"} {
		if _, exists := workspace[forbidden]; exists {
			t.Fatalf("LocalWorkspace must not expose internal field %q", forbidden)
		}
	}
	status := workspace["status"].(map[string]any)
	if got := status["enum"]; !reflect.DeepEqual(got, []any{"active", "revoked", "path_unavailable"}) {
		t.Fatalf("LocalWorkspace.status enum=%#v", got)
	}
	readPolicy := workspace["read_policy"].(map[string]any)
	if got := readPolicy["enum"]; !reflect.DeepEqual(got, []any{"allow"}) {
		t.Fatalf("LocalWorkspace.read_policy enum=%#v", got)
	}
	writePolicy := workspace["write_policy"].(map[string]any)
	if got := writePolicy["enum"]; !reflect.DeepEqual(got, []any{"allow"}) {
		t.Fatalf("LocalWorkspace.write_policy enum=%#v", got)
	}

	revokeRequest := schemaPropertiesForTest(t, schemas, "LocalWorkspaceRevokeRequest")
	if _, exists := revokeRequest["version"]; !exists {
		t.Fatal("LocalWorkspaceRevokeRequest.version missing")
	}
	revokeResponse := schemaPropertiesForTest(t, schemas, "LocalWorkspaceRevokeResponse")
	if _, exists := revokeResponse["affected_task_count"]; !exists {
		t.Fatal("LocalWorkspaceRevokeResponse.affected_task_count missing")
	}
	bindingResponse := schemaPropertiesForTest(t, schemas, "ConversationLocalWorkspaceResponse")
	if _, exists := bindingResponse["status"]; !exists {
		t.Fatal("ConversationLocalWorkspaceResponse.status missing")
	}
}
