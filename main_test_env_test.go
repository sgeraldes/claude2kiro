package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/profile"
)

// The launched profile is read once, during package initialisation (the
// credit recorder resolves its file then), so a CLAUDE2KIRO_PROFILE inherited
// from the shell would make the whole suite run as that profile: the attach
// tests, the end-to-end tests and the failover fixtures all describe the
// unnamed profile. Say so once instead of failing twenty tests in confusing
// ways.
func TestMain(m *testing.M) {
	if v := os.Getenv(profile.EnvVar); v != "" {
		fmt.Fprintf(os.Stderr, "%s=%q is set in the environment: this test suite runs as the unnamed profile. Unset it and run again.\n", profile.EnvVar, v)
		os.Exit(2)
	}
	os.Exit(m.Run())
}
