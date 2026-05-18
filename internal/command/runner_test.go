package command

import "testing"

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
