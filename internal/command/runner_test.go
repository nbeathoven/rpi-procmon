package command

import "testing"

func TestExitCodeDescriptionIncludesCommonSignals(t *testing.T) {
	got := ExitCodeDescription(143)
	want := "exit code 143 (terminated by SIGTERM)"
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
