package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/state"
)

type SnapshotProvider interface {
	Snapshot() state.ProcmonStatus
	MonitorSnapshot(id string) (*state.MonitorRuntimeState, bool)
}

func NewServer(cfg config.Config, provider SnapshotProvider) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		snapshot := provider.Snapshot()
		ok := overallStatusHealthy(snapshot.OverallStatus)
		statusCode := http.StatusOK
		if !ok {
			statusCode = http.StatusServiceUnavailable
		}
		writeJSON(w, statusCode, map[string]any{
			"alive":          true,
			"ok":             ok,
			"overall_status": snapshot.OverallStatus,
			"app_version":    snapshot.AppVersion,
			"uptime_seconds": snapshot.UptimeSeconds,
			"monitor_count":  snapshot.MonitorCount,
		})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, provider.Snapshot())
	})
	mux.HandleFunc("/monitors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, provider.Snapshot().Monitors)
	})
	mux.HandleFunc("/monitors/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/monitors/")
		if id == "" {
			http.NotFound(w, r)
			return
		}
		monitor, ok := provider.MonitorSnapshot(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, monitor)
	})

	return &http.Server{
		Addr:              cfg.API.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: parseDuration(cfg.API.ReadHeaderTimeout, 5*time.Second),
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func overallStatusHealthy(status string) bool {
	switch status {
	case "healthy", "recovering":
		return true
	default:
		return false
	}
}

func parseDuration(value string, def time.Duration) time.Duration {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return def
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return def
	}
	return parsed
}
