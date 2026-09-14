package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
)

// TestRunNotificationFixtureProcess is a subprocess entry point, not a product
// feature switch. It invokes the normal startup, SQL migrations and workers.
// The Python acceptance fixture supplies disposable DBs and local upstreams.
func TestRunNotificationFixtureProcess(t *testing.T) {
	if os.Getenv("NOTIFICATION_FIXTURE_PROCESS") != "1" {
		t.Skip("subprocess fixture; invoked by notification E2E tests")
	}
	if err := validateStartupConfig(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		t.Fatal(err)
	}
}
