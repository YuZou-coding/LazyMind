package localworkspace

import "testing"

func TestWorkspacePermissionPolicyCoversNetworkAndConnectedApps(t *testing.T) {
	for _, operationClass := range []string{"network", "connected_app"} {
		if !allowedOperationClass(operationClass) {
			t.Fatalf("operation class %q must be resolved through the bound task permission mode", operationClass)
		}
	}
}
