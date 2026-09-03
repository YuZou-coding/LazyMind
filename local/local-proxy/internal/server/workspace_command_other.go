//go:build !darwin

package server

import "context"

func runContainedWorkspaceCommand(_ context.Context, _, _ string, _ []string, _ map[string]string) (workspaceCommandResult, error) {
	return workspaceCommandResult{}, errWorkspaceContainmentUnavailable
}
