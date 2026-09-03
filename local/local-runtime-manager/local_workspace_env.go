package main

import (
	"os"
	"strings"
)

const localWorkspaceHostTokenEnvVar = "LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN"

func localWorkspaceRuntime(cfg RuntimeConfig) string {
	if cfg.Profile == "desktop" {
		return "desktop"
	}
	if cfg.Profile == "local" && cfg.NetworkProfile == "localhost" && !cfg.AuthServiceAllowsLAN() {
		return "local"
	}
	return "disabled"
}

func localWorkspaceHostToken(cfg RuntimeConfig, paths RuntimePaths) string {
	if cfg.Profile == "desktop" && strings.TrimSpace(cfg.OwnerToken) != "" {
		return strings.TrimSpace(cfg.OwnerToken)
	}
	if raw, err := os.ReadFile(paths.RunDirTokenFile); err == nil {
		if token := strings.TrimSpace(string(raw)); token != "" {
			return token
		}
	}
	return strings.TrimSpace(cfg.OwnerToken)
}

func (cfg RuntimeConfig) AuthServiceAllowsLAN() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(localAutoLoginAllowLANEnvVar)), "true") ||
		strings.TrimSpace(cfg.LocalProxy.Address) == "0.0.0.0"
}
