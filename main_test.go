package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckHealthRequireSerialFailsOnInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	ok, reason := checkHealth(config{
		healthURL:        server.URL,
		healthTimeoutSec: 1,
		requireSerial:    true,
	})

	if ok {
		t.Fatal("expected health check to fail")
	}
	if !strings.Contains(reason, "invalid JSON") {
		t.Fatalf("expected invalid JSON reason, got %q", reason)
	}
}

func TestCheckHealthRequireSerialFailsOnMissingField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	ok, reason := checkHealth(config{
		healthURL:        server.URL,
		healthTimeoutSec: 1,
		requireSerial:    true,
	})

	if ok {
		t.Fatal("expected health check to fail")
	}
	if reason != "health response missing serial_connected" {
		t.Fatalf("unexpected reason %q", reason)
	}
}

func TestCheckHealthRequireSerialPassesWhenConnected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"serial_connected":true}`))
	}))
	defer server.Close()

	ok, reason := checkHealth(config{
		healthURL:        server.URL,
		healthTimeoutSec: 1,
		requireSerial:    true,
	})

	if !ok {
		t.Fatalf("expected health check to pass, got %q", reason)
	}
}

func TestTriggerRebootDoesNotWriteStateOnFailure(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var logs bytes.Buffer

	err := triggerReboot(config{
		rebootCmd: "false",
		stateFile: statePath,
	}, &logs, state{}, "health check failed")
	if err == nil {
		t.Fatal("expected reboot to fail")
	}

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected no state file to be written, got err=%v", err)
	}
}

func TestTriggerRebootWritesStateOnSuccess(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var logs bytes.Buffer

	err := triggerReboot(config{
		rebootCmd: "true",
		stateFile: statePath,
	}, &logs, state{RebootCount: 2}, "load high")
	if err != nil {
		t.Fatalf("expected reboot command to succeed, got %v", err)
	}

	st, err := readState(statePath)
	if err != nil {
		t.Fatalf("expected state to be written, got %v", err)
	}
	if st.RebootCount != 3 {
		t.Fatalf("expected reboot count 3, got %d", st.RebootCount)
	}
	if st.LastReason != "load high" {
		t.Fatalf("expected last reason to be persisted, got %q", st.LastReason)
	}
	if st.LastRebootAt == "" {
		t.Fatal("expected last reboot time to be set")
	}
}
