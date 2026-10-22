package command

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestShellRunnerCancelsDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := (ShellRunner{}).Run(ctx, "sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) || out.ExitCode == 0 {
		t.Fatalf("expected deadline failure, got %+v %v", out, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("child process kept command blocked after deadline")
	}
}

func TestShellRunnerRejectsTruncatedOutput(t *testing.T) {
	out, err := (ShellRunner{}).Run(context.Background(), "head -c 8388609 /dev/zero")
	if err == nil || out.ExitCode == 0 || !strings.Contains(out.Output, "output exceeds") {
		t.Fatalf("expected explicit overflow failure: exit=%d err=%v", out.ExitCode, err)
	}
	if len(out.Output) > maxOutputBytes+128 {
		t.Fatal("output cap was exceeded")
	}
}

func TestShellRunnerPreservesSuccessfulOutput(t *testing.T) {
	out, err := (ShellRunner{}).Run(context.Background(), "printf healthy")
	if err != nil || out.ExitCode != 0 || out.Output != "healthy" {
		t.Fatalf("unexpected result: %+v %v", out, err)
	}
}

func TestExitCodeDescriptionIncludesCommonSignals(t *testing.T) {
	got := ExitCodeDescription(143)
	want := "exit code 143 (terminated by SIGTERM; asked to stop)"
	if got != want {
		t.Fatalf("ExitCodeDescription(143) = %q, want %q", got, want)
	}
}

func TestExitCodeDescriptionKeepsPlainExitCodes(t *testing.T) {
	got := ExitCodeDescription(2)
	want := "exit code 2"
	if got != want {
		t.Fatalf("ExitCodeDescription(2) = %q, want %q", got, want)
	}
}

func TestSSHFailureMessageExplainsExit255(t *testing.T) {
	got := SSHFailureMessage("restart_systemd_service", 255)
	want := "restart_systemd_service failed with exit code 255 (SSH connection/authentication failure; the remote command may not have run)"
	if got != want {
		t.Fatalf("SSHFailureMessage = %q, want %q", got, want)
	}
}
