package engine

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/actions"
	"github.com/nbeathoven/rpi-procmon/internal/checks"
	"github.com/nbeathoven/rpi-procmon/internal/command"
	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/events"
	"github.com/nbeathoven/rpi-procmon/internal/logging"
	"github.com/nbeathoven/rpi-procmon/internal/state"
)

type Manager struct {
	cfg        config.Config
	logger     io.Writer
	runner     command.Runner
	store      state.Store
	eventStore events.Store
	startedAt  time.Time
	appVersion string

	mu           sync.RWMutex
	current      state.ProcmonState
	eventHistory events.History
}

func NewManager(cfg config.Config, logger io.Writer, runner command.Runner, appVersion string) (*Manager, error) {
	startedAt := time.Now().UTC()
	store := state.FileStore{Path: cfg.StateFile}
	eventStore := events.FileStore{Path: cfg.EventsFile, MaxEntries: cfg.EventsMaxEntries}
	snapshot, err := store.Load()
	if err != nil {
		return nil, err
	}
	eventHistory, err := eventStore.Load()
	if err != nil {
		return nil, err
	}
	if snapshot.Monitors == nil {
		snapshot.Monitors = make(map[string]*state.MonitorRuntimeState)
	}
	snapshot.StartedAt = startedAt.Format(time.RFC3339)
	snapshot.Version = state.SchemaVersion
	snapshot.AppVersion = appVersion

	manager := &Manager{
		cfg:        cfg,
		logger:     logger,
		runner:     runner,
		store:      store,
		eventStore: eventStore,
		startedAt:  startedAt,
		appVersion: appVersion,
		current:    snapshot,
		eventHistory: eventHistory,
	}

	manager.mu.Lock()
	for _, monitor := range cfg.Monitors {
		st := manager.ensureMonitorStateLocked(monitor)
		if !monitor.IsEnabled() {
			st.Status = "disabled"
		} else if st.Status == "" {
			st.Status = "unknown"
		}
	}
	err = manager.persistLocked()
	manager.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Start(ctx context.Context) {
	for _, monitor := range m.cfg.Monitors {
		if !monitor.IsEnabled() {
			continue
		}
		go m.runMonitorLoop(ctx, monitor)
	}
}

func (m *Manager) Snapshot() state.ProcmonStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	monitors := make([]*state.MonitorRuntimeState, 0, len(m.cfg.Monitors))
	statusValue := "healthy"
	var healthy, degraded, recovering, failed, disabled int

	for _, cfgMonitor := range m.cfg.Monitors {
		monitorState, ok := m.current.Monitors[cfgMonitor.ID]
		if !ok {
			continue
		}
		clone := state.CloneMonitorState(monitorState)
		monitors = append(monitors, clone)
		switch clone.Status {
		case "healthy":
			healthy++
		case "recovering", "checking":
			recovering++
			if statusValue == "healthy" {
				statusValue = "recovering"
			}
		case "degraded":
			degraded++
			if statusValue == "healthy" {
				statusValue = "degraded"
			}
		case "failed":
			failed++
			statusValue = "failed"
		case "disabled":
			disabled++
		default:
			if statusValue == "healthy" {
				statusValue = "unknown"
			}
		}
	}

	return state.ProcmonStatus{
		AppVersion:      m.appVersion,
		StartedAt:       m.current.StartedAt,
		UptimeSeconds:   int64(time.Since(m.startedAt).Seconds()),
		OverallStatus:   statusValue,
		MonitorCount:    len(monitors),
		HealthyCount:    healthy,
		DegradedCount:   degraded,
		RecoveringCount: recovering,
		FailedCount:     failed,
		DisabledCount:   disabled,
		ConfigFile:      m.cfg.ConfigFile,
		StateFile:       m.cfg.StateFile,
		EventsFile:      m.cfg.EventsFile,
		LogFile:         m.cfg.LogFile,
		ListenAddress:   m.cfg.API.ListenAddress,
		LastUpdatedAt:   m.current.LastUpdatedAt,
		Monitors:        monitors,
	}
}

func (m *Manager) MonitorSnapshot(id string) (*state.MonitorRuntimeState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	monitorState, ok := m.current.Monitors[id]
	if !ok {
		return nil, false
	}
	return state.CloneMonitorState(monitorState), true
}

func (m *Manager) EventsSnapshot(filter events.Filter) []events.Event {
	m.mu.RLock()
	defer m.mu.RUnlock()

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	result := make([]events.Event, 0, limit)
	for i := len(m.eventHistory.Events) - 1; i >= 0; i-- {
		event := m.eventHistory.Events[i]
		if filter.MonitorID != "" && event.MonitorID != filter.MonitorID {
			continue
		}
		if filter.EventType != "" && event.EventType != filter.EventType {
			continue
		}
		if !filter.Since.IsZero() {
			timestamp, err := time.Parse(time.RFC3339, event.Timestamp)
			if err != nil || timestamp.Before(filter.Since) {
				continue
			}
		}
		result = append(result, events.CloneEvent(event))
		if len(result) >= limit {
			break
		}
	}
	return result
}

func (m *Manager) runMonitorLoop(ctx context.Context, monitor config.MonitorConfig) {
	interval := parseDuration(monitor.Interval, time.Minute)
	m.runMonitorCycle(ctx, monitor, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runMonitorCycle(ctx, monitor, interval)
		}
	}
}

func (m *Manager) runMonitorCycle(ctx context.Context, monitor config.MonitorConfig, interval time.Duration) {
	start := time.Now().UTC()
	statusBeforeCheck := ""

	m.mu.Lock()
	monitorState := m.ensureMonitorStateLocked(monitor)
	statusBeforeCheck = monitorState.Status
	monitorState.Status = "checking"
	monitorState.LastCheckStartedAt = start.Format(time.RFC3339)
	monitorState.NextCheckAt = start.Add(interval).Format(time.RFC3339)
	m.persistOrLogLocked()
	m.mu.Unlock()

	results, reasons := m.evaluateMonitor(ctx, monitor)
	finished := time.Now().UTC()
	durationMS := finished.Sub(start).Milliseconds()

	m.mu.Lock()
	monitorState = m.ensureMonitorStateLocked(monitor)
	monitorState.CheckRunCount++
	monitorState.LastCheckFinishedAt = finished.Format(time.RFC3339)
	monitorState.LastCheckDurationMS = durationMS
	monitorState.LastCheckResults = state.CloneCheckResults(results)

	if len(reasons) == 0 {
		monitorState.Status = "healthy"
		monitorState.ConsecutiveFailures = 0
		monitorState.SuccessCount++
		monitorState.LastSuccessAt = finished.Format(time.RFC3339)
		monitorState.LastError = ""
		monitorState.LastFailureReasons = nil
		if shouldRecordHealthyEvent(statusBeforeCheck) {
			m.appendEventLocked(events.Event{
				ID:          eventID(monitor.ID, finished),
				Timestamp:   finished.Format(time.RFC3339),
				MonitorID:   monitor.ID,
				MonitorName: monitor.Name,
				MonitorType: monitor.Type,
				EventType:   "check_succeeded",
				StatusBefore: statusBeforeCheck,
				StatusAfter: "healthy",
				CheckResults: results,
			})
		}
		m.persistOrLogLocked()
		m.mu.Unlock()
		logging.Logf(m.logger, "monitor check ok: id=%s type=%s duration_ms=%d checks=%d", monitor.ID, monitor.Type, durationMS, len(results))
		return
	}

	monitorState.Status = "degraded"
	monitorState.FailureCount++
	monitorState.ConsecutiveFailures++
	monitorState.LastFailureAt = finished.Format(time.RFC3339)
	monitorState.LastError = joinReasons(reasons)
	monitorState.LastFailureReasons = append([]string(nil), reasons...)
	shouldRecover := monitorState.ConsecutiveFailures >= monitor.FailureThreshold && !cooldownActive(monitorState.CooldownUntil, finished)
	consecutiveFailures := monitorState.ConsecutiveFailures
	m.appendEventLocked(events.Event{
		ID:                  eventID(monitor.ID, finished),
		Timestamp:           finished.Format(time.RFC3339),
		MonitorID:           monitor.ID,
		MonitorName:         monitor.Name,
		MonitorType:         monitor.Type,
		EventType:           "check_failed",
		StatusBefore:        statusBeforeCheck,
		StatusAfter:         "degraded",
		Reason:              joinReasons(reasons),
		ConsecutiveFailures: monitorState.ConsecutiveFailures,
		CheckResults:        results,
	})
	m.persistOrLogLocked()
	m.mu.Unlock()

	logging.Logf(m.logger, "monitor check failed: id=%s type=%s duration_ms=%d consecutive_failures=%d threshold=%d will_recover=%t reasons=%s", monitor.ID, monitor.Type, durationMS, consecutiveFailures, monitor.FailureThreshold, shouldRecover, joinReasons(reasons))

	if !shouldRecover {
		return
	}

	actionResults, recovered, postResults, postReasons := m.executeRecovery(ctx, monitor, results, reasons)

	m.mu.Lock()
	monitorState = m.ensureMonitorStateLocked(monitor)
	now := time.Now().UTC()
	monitorState.CooldownUntil = now.Add(parseDuration(monitor.Cooldown, 5*time.Minute)).Format(time.RFC3339)
	monitorState.LastRecoveryResults = state.CloneActionResults(actionResults)
	monitorState.RecoveryCount++
	if recovered {
		monitorState.Status = "healthy"
		monitorState.ConsecutiveFailures = 0
		monitorState.LastRecoverySuccessAt = now.Format(time.RFC3339)
		monitorState.LastSuccessAt = now.Format(time.RFC3339)
		monitorState.LastError = ""
		monitorState.LastFailureReasons = nil
		if len(postResults) > 0 {
			monitorState.LastCheckResults = state.CloneCheckResults(postResults)
		}
		m.appendEventLocked(events.Event{
			ID:              eventID(monitor.ID, now),
			Timestamp:       now.Format(time.RFC3339),
			MonitorID:       monitor.ID,
			MonitorName:     monitor.Name,
			MonitorType:     monitor.Type,
			EventType:       "recovery_succeeded",
			StatusBefore:    "recovering",
			StatusAfter:     "healthy",
			RecoveryCount:   monitorState.RecoveryCount,
			CheckResults:    postResults,
			RecoveryResults: actionResults,
		})
	} else {
		monitorState.Status = "failed"
		monitorState.RecoveryFailureCount++
		monitorState.LastRecoveryFailureAt = now.Format(time.RFC3339)
		if len(postResults) > 0 {
			monitorState.LastCheckResults = state.CloneCheckResults(postResults)
		}
		if len(postReasons) > 0 {
			monitorState.LastError = joinReasons(postReasons)
			monitorState.LastFailureReasons = append([]string(nil), postReasons...)
		}
		m.appendEventLocked(events.Event{
			ID:                  eventID(monitor.ID, now),
			Timestamp:           now.Format(time.RFC3339),
			MonitorID:           monitor.ID,
			MonitorName:         monitor.Name,
			MonitorType:         monitor.Type,
			EventType:           "recovery_failed",
			StatusBefore:        "recovering",
			StatusAfter:         "failed",
			Reason:              joinReasons(postReasons),
			ConsecutiveFailures: monitorState.ConsecutiveFailures,
			RecoveryCount:       monitorState.RecoveryCount,
			CheckResults:        postResults,
			RecoveryResults:     actionResults,
		})
	}
	m.persistOrLogLocked()
	m.mu.Unlock()

	if recovered {
		logging.Logf(m.logger, "monitor recovery succeeded: id=%s actions=%d", monitor.ID, len(actionResults))
		return
	}
	if len(postReasons) > 0 {
		logging.Logf(m.logger, "monitor recovery failed: id=%s actions=%d reasons=%s", monitor.ID, len(actionResults), joinReasons(postReasons))
		return
	}
	logging.Logf(m.logger, "monitor recovery failed: id=%s actions=%d", monitor.ID, len(actionResults))
}

func (m *Manager) evaluateMonitor(ctx context.Context, monitor config.MonitorConfig) ([]state.CheckResult, []string) {
	results := make([]state.CheckResult, 0, len(monitor.Checks))
	reasons := make([]string, 0)
	for _, check := range monitor.Checks {
		result := checks.Run(ctx, m.runner, monitor, check)
		results = append(results, result)
		if !result.Success {
			reasons = append(reasons, result.Message)
		}
	}
	return results, reasons
}

func (m *Manager) executeRecovery(ctx context.Context, monitor config.MonitorConfig, failureResults []state.CheckResult, failureReasons []string) ([]state.ActionResult, bool, []state.CheckResult, []string) {
	results := make([]state.ActionResult, 0, len(monitor.Recovery))

	m.mu.Lock()
	monitorState := m.ensureMonitorStateLocked(monitor)
	monitorState.Status = "recovering"
	now := time.Now().UTC()
	monitorState.LastRecoveryAttemptAt = now.Format(time.RFC3339)
	m.appendEventLocked(events.Event{
		ID:                  eventID(monitor.ID, now),
		Timestamp:           now.Format(time.RFC3339),
		MonitorID:           monitor.ID,
		MonitorName:         monitor.Name,
		MonitorType:         monitor.Type,
		EventType:           "recovery_started",
		StatusBefore:        "degraded",
		StatusAfter:         "recovering",
		Reason:              joinReasons(failureReasons),
		ConsecutiveFailures: monitorState.ConsecutiveFailures,
		CheckResults:        failureResults,
	})
	m.persistOrLogLocked()
	m.mu.Unlock()
	logging.Logf(m.logger, "monitor recovery start: id=%s actions=%d", monitor.ID, len(monitor.Recovery))

	for _, action := range monitor.Recovery {
		actionResult := actions.Run(ctx, m.runner, action)
		results = append(results, actionResult)
		logging.Logf(m.logger, "monitor recovery action: id=%s action=%s type=%s success=%t message=%s", monitor.ID, actionResult.Name, actionResult.Type, actionResult.Success, actionResult.Message)

		if action.Type == "recheck" {
			checkResults, reasons := m.evaluateMonitor(ctx, monitor)
			if len(reasons) == 0 {
				return results, true, checkResults, nil
			}
			continue
		}
		if !actionResult.Success {
			return results, false, nil, []string{actionResult.Message}
		}
	}

	checkResults, reasons := m.evaluateMonitor(ctx, monitor)
	return results, len(reasons) == 0, checkResults, reasons
}

func (m *Manager) ensureMonitorStateLocked(monitor config.MonitorConfig) *state.MonitorRuntimeState {
	if existing, ok := m.current.Monitors[monitor.ID]; ok {
		existing.ID = monitor.ID
		existing.Name = monitor.Name
		existing.Type = monitor.Type
		existing.Enabled = monitor.IsEnabled()
		existing.Interval = monitor.Interval
		existing.FailureThreshold = monitor.FailureThreshold
		existing.Cooldown = monitor.Cooldown
		existing.Metadata = cloneStringMap(monitor.Metadata)
		existing.ConfiguredChecks = cloneCheckConfigs(monitor.Checks)
		existing.ConfiguredRecoveries = cloneActionConfigs(monitor.Recovery)
		return existing
	}

	monitorState := &state.MonitorRuntimeState{
		ID:                   monitor.ID,
		Name:                 monitor.Name,
		Type:                 monitor.Type,
		Enabled:              monitor.IsEnabled(),
		Status:               "unknown",
		Interval:             monitor.Interval,
		FailureThreshold:     monitor.FailureThreshold,
		Cooldown:             monitor.Cooldown,
		Metadata:             cloneStringMap(monitor.Metadata),
		ConfiguredChecks:     cloneCheckConfigs(monitor.Checks),
		ConfiguredRecoveries: cloneActionConfigs(monitor.Recovery),
	}
	m.current.Monitors[monitor.ID] = monitorState
	return monitorState
}

func (m *Manager) persistOrLogLocked() {
	if err := m.persistLocked(); err != nil {
		logging.Logf(m.logger, "state persist failed: %v", err)
	}
}

func (m *Manager) persistLocked() error {
	m.current.Version = state.SchemaVersion
	m.current.AppVersion = m.appVersion
	m.current.LastUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := m.store.Save(m.current); err != nil {
		return err
	}
	m.eventHistory.Version = events.SchemaVersion
	return m.eventStore.Save(m.eventHistory)
}

func (m *Manager) appendEventLocked(event events.Event) {
	event.CheckResults = state.CloneCheckResults(event.CheckResults)
	event.RecoveryResults = state.CloneActionResults(event.RecoveryResults)
	m.eventHistory.Events = append(m.eventHistory.Events, event)
	if limit := m.cfg.EventsMaxEntries; limit > 0 && len(m.eventHistory.Events) > limit {
		start := len(m.eventHistory.Events) - limit
		trimmed := make([]events.Event, limit)
		copy(trimmed, m.eventHistory.Events[start:])
		m.eventHistory.Events = trimmed
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

func cooldownActive(until string, now time.Time) bool {
	if until == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return false
	}
	return parsed.After(now)
}

func joinReasons(reasons []string) string {
	result := ""
	for i, reason := range reasons {
		if i > 0 {
			result += "; "
		}
		result += reason
	}
	return result
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneCheckConfigs(in []config.CheckConfig) []config.CheckConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]config.CheckConfig, len(in))
	copy(out, in)
	return out
}

func cloneActionConfigs(in []config.ActionConfig) []config.ActionConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]config.ActionConfig, len(in))
	copy(out, in)
	return out
}

func shouldRecordHealthyEvent(statusBefore string) bool {
	switch statusBefore {
	case "degraded", "failed", "recovering":
		return true
	default:
		return false
	}
}

func eventID(monitorID string, timestamp time.Time) string {
	return fmt.Sprintf("%s-%d", monitorID, timestamp.UTC().UnixNano())
}
