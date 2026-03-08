package events

import (
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/state"
)

const SchemaVersion = 1

type History struct {
	Version int     `json:"version"`
	Events  []Event `json:"events"`
}

type Event struct {
	ID                  string               `json:"id"`
	Timestamp           string               `json:"timestamp"`
	MonitorID           string               `json:"monitor_id"`
	MonitorName         string               `json:"monitor_name"`
	MonitorType         string               `json:"monitor_type"`
	EventType           string               `json:"event_type"`
	StatusBefore        string               `json:"status_before,omitempty"`
	StatusAfter         string               `json:"status_after,omitempty"`
	Reason              string               `json:"reason,omitempty"`
	ConsecutiveFailures int                  `json:"consecutive_failures,omitempty"`
	RecoveryCount       int                  `json:"recovery_count,omitempty"`
	CheckResults        []state.CheckResult  `json:"check_results,omitempty"`
	RecoveryResults     []state.ActionResult `json:"recovery_results,omitempty"`
}

type Filter struct {
	MonitorID string
	EventType string
	Since     time.Time
	Limit     int
}

func CloneEvent(in Event) Event {
	out := in
	out.CheckResults = state.CloneCheckResults(in.CheckResults)
	out.RecoveryResults = state.CloneActionResults(in.RecoveryResults)
	return out
}

func CloneEvents(in []Event) []Event {
	if len(in) == 0 {
		return nil
	}
	out := make([]Event, len(in))
	for i := range in {
		out[i] = CloneEvent(in[i])
	}
	return out
}
