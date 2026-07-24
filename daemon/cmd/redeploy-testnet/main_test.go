package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunForgeCommandDoesNotMarkStartWhenExecutableIsMissing(t *testing.T) {
	started := false
	command := exec.Command(filepath.Join(t.TempDir(), "missing-forge"))

	err := runForgeCommand(command, func() { started = true })

	if err == nil || !strings.Contains(err.Error(), "starting forge deployment") {
		t.Fatalf("expected start failure, got %v", err)
	}
	if started {
		t.Fatal("missing executable was marked as a potentially broadcast deployment")
	}
}

func TestRunForgeCommandMarksStartBeforeWaiting(t *testing.T) {
	started := false
	command := exec.Command(os.Args[0], "-test.run=TestRunForgeCommandHelper")
	command.Env = append(os.Environ(), "SNAPFALL_FORGE_HELPER=1")

	err := runForgeCommand(command, func() { started = true })

	if err == nil || !strings.Contains(err.Error(), "forge deployment") {
		t.Fatalf("expected post-start command failure, got %v", err)
	}
	if !started {
		t.Fatal("started command was not marked as a potentially broadcast deployment")
	}
}

func TestRunForgeCommandHelper(t *testing.T) {
	if os.Getenv("SNAPFALL_FORGE_HELPER") != "1" {
		return
	}
	os.Exit(7)
}
