package api

import (
	"embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/events"
	"github.com/nbeathoven/rpi-procmon/internal/state"
)

//go:embed ui/index.html
var uiFS embed.FS

type SnapshotProvider interface {
	Snapshot() state.ProcmonStatus
	MonitorSnapshot(id string) (*state.MonitorRuntimeState, bool)
	EventsSnapshot(events.Filter) []events.Event
}

func NewServer(cfg config.Config, provider SnapshotProvider) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/ui/", serveUI)
	mux.HandleFunc("/ui/index.html", serveUI)
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
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		filter, err := parseEventFilter(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		result := provider.EventsSnapshot(filter)
		writeJSON(w, http.StatusOK, map[string]any{
			"events":      result,
			"returned":    len(result),
			"monitor_id":  filter.MonitorID,
			"event_type":  filter.EventType,
			"limit":       normalizeEventLimit(filter.Limit),
			"since":       formatFilterSince(filter.Since),
		})
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
		Handler:           withCORS(mux),
		ReadHeaderTimeout: parseDuration(cfg.API.ReadHeaderTimeout, 5*time.Second),
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ui/" && r.URL.Path != "/ui/index.html" {
		http.NotFound(w, r)
		return
	}
	data, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func parseEventFilter(r *http.Request) (events.Filter, error) {
	query := r.URL.Query()
	filter := events.Filter{
		MonitorID: strings.TrimSpace(query.Get("monitor_id")),
		EventType: strings.TrimSpace(query.Get("event_type")),
		Limit:     normalizeEventLimit(0),
	}
	if value := strings.TrimSpace(query.Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return events.Filter{}, err
		}
		filter.Limit = normalizeEventLimit(parsed)
	}
	if value := strings.TrimSpace(query.Get("since")); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return events.Filter{}, err
		}
		filter.Since = parsed
	}
	return filter, nil
}

func normalizeEventLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func formatFilterSince(since time.Time) string {
	if since.IsZero() {
		return ""
	}
	return since.UTC().Format(time.RFC3339)
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
