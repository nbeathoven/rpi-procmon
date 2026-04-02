package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/engine"
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

type ControlProvider interface {
	TriggerCheck(context.Context, string) (*state.MonitorRuntimeState, error)
	TriggerRecovery(context.Context, string) (*state.MonitorRuntimeState, error)
}

type apiAuth struct {
	adminToken string
}

func NewServer(cfg config.Config, provider SnapshotProvider) *http.Server {
	mux := http.NewServeMux()
	controlProvider, _ := provider.(ControlProvider)
	auth := apiAuth{adminToken: strings.TrimSpace(cfg.API.AdminToken)}

	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/ui/", serveUI)
	mux.HandleFunc("/ui/index.html", serveUI)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		serveHealth(w, provider)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, provider.Snapshot())
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		serveEvents(w, r, provider)
	})
	mux.HandleFunc("/monitors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, provider.Snapshot().Monitors)
	})
	mux.HandleFunc("/monitors/", func(w http.ResponseWriter, r *http.Request) {
		serveMonitorRoute(w, r, provider, controlProvider, auth, strings.TrimPrefix(r.URL.Path, "/monitors/"))
	})

	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		serveHealth(w, provider)
	})
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, provider.Snapshot())
	})
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		serveEvents(w, r, provider)
	})
	mux.HandleFunc("/api/v1/monitors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, provider.Snapshot().Monitors)
	})
	mux.HandleFunc("/api/v1/monitors/", func(w http.ResponseWriter, r *http.Request) {
		serveMonitorRoute(w, r, provider, controlProvider, auth, strings.TrimPrefix(r.URL.Path, "/api/v1/monitors/"))
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type")
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

func serveHealth(w http.ResponseWriter, provider SnapshotProvider) {
	snapshot := provider.Snapshot()
	ok := overallStatusHealthy(snapshot.OverallStatus)
	statusCode := http.StatusOK
	if !ok {
		statusCode = http.StatusServiceUnavailable
	}
	writeJSON(w, statusCode, map[string]any{
		"alive":               true,
		"ok":                  ok,
		"overall_status":      snapshot.OverallStatus,
		"app_version":         snapshot.AppVersion,
		"uptime_seconds":      snapshot.UptimeSeconds,
		"monitor_count":       snapshot.MonitorCount,
		"control_api_enabled": snapshot.ControlAPIEnabled,
	})
}

func serveEvents(w http.ResponseWriter, r *http.Request, provider SnapshotProvider) {
	filter, err := parseEventFilter(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	result := provider.EventsSnapshot(filter)
	writeJSON(w, http.StatusOK, map[string]any{
		"events":     result,
		"returned":   len(result),
		"monitor_id": filter.MonitorID,
		"event_type": filter.EventType,
		"limit":      normalizeEventLimit(filter.Limit),
		"since":      formatFilterSince(filter.Since),
	})
}

func serveMonitorRoute(w http.ResponseWriter, r *http.Request, provider SnapshotProvider, controlProvider ControlProvider, auth apiAuth, path string) {
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(path, "/")
	if len(parts) == 1 && r.Method == http.MethodGet {
		monitor, ok := provider.MonitorSnapshot(parts[0])
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, monitor)
		return
	}

	if len(parts) != 2 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if controlProvider == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "control api unavailable"})
		return
	}
	if auth.adminToken == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "control api disabled"})
		return
	}
	if !auth.authorizeAdmin(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	id := parts[0]
	action := parts[1]
	var (
		monitor *state.MonitorRuntimeState
		err     error
	)
	switch action {
	case "check":
		monitor, err = controlProvider.TriggerCheck(ctx, id)
	case "recover":
		monitor, err = controlProvider.TriggerRecovery(ctx, id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, engine.ErrMonitorNotFound):
			status = http.StatusNotFound
		case errors.Is(err, engine.ErrMonitorDisabled):
			status = http.StatusConflict
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"action":  action,
		"monitor": monitor,
	})
}

func (a apiAuth) authorizeAdmin(r *http.Request) bool {
	if a.adminToken == "" {
		return false
	}
	token := bearerToken(r.Header.Get("Authorization"))
	return token != "" && token == a.adminToken
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
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
