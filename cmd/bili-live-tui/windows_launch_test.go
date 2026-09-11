package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestRootCommandAllowsExplorerLaunch(t *testing.T) {
	previous := cobra.MousetrapHelpText
	t.Cleanup(func() { cobra.MousetrapHelpText = previous })
	cobra.MousetrapHelpText = "Explorer launch blocked"
	cmd := newRootCmd()
	if cobra.MousetrapHelpText != "" {
		t.Fatal("Cobra must not block double-click launch before the TUI starts")
	}
	if cmd.RunE == nil {
		t.Fatal("the default command must retain its interactive entry point")
	}
}
