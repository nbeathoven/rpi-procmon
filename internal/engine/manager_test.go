package engine

import (
	"context"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/command"
	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/events"
	"github.com/nbeathoven/rpi-procmon/internal/state"
)

type countingStateStore struct {
	saves int
	err   error
	last  state.ProcmonState
}

func (s *countingStateStore) Load() (state.ProcmonState, error) {
	return state.ProcmonState{}, nil
}

func (s *countingStateStore) Save(snapshot state.ProcmonState) error {
	s.saves++
	s.last = state.ProcmonState{Monitors: make(map[string]*state.MonitorRuntimeState)}
	for id, monitor := range snapshot.Monitors {
		s.last.Monitors[id] = state.CloneMonitorState(monitor)
	}
	return s.err
}

type runnerFunc func(context.Context, string) (command.Outcome, error)

func (f runnerFunc) Run(ctx context.Context, cmd string) (command.Outcome, error) {
	return f(ctx, cmd)
}

func testManager(runner command.Runner) (*Manager, config.MonitorConfig) {
	monitor := config.MonitorConfig{
		ID: "test", Interval: "1m", Cooldown: "5m", FailureThreshold: 1,
		Checks:   []config.CheckConfig{{Type: "command", Command: "check"}},
		Recovery: []config.ActionConfig{{Type: "command", Command: "recover"}},
	}
	m := &Manager{
		cfg:    config.Config{Monitors: []config.MonitorConfig{monitor}},
		logger: io.Discard, runner: runner,
		store: &countingStateStore{}, eventStore: &countingEventStore{},
		current: state.ProcmonState{Monitors: make(map[string]*state.MonitorRuntimeState)},
	}
	m.ensureMonitorStateLocked(monitor).Status = "healthy"
	return m, monitor
}

func TestSnapshotAggregatePrecedence(t *testing.T) {
	// Ascending precedence; checking counts as recovering, while disabled is neutral.
	statuses := []struct {
		status  string
		overall string
		rank    int
	}{
		{"disabled", "healthy", 0},
		{"healthy", "healthy", 0},
		{"checking", "recovering", 1},
		{"recovering", "recovering", 1},
		{"unknown", "unknown", 2},
		{"future-status", "unknown", 2},
		{"degraded", "degraded", 3},
		{"failed", "failed", 4},
	}
	for _, first := range statuses {
		for _, second := range statuses {
			t.Run(first.status+"/"+second.status, func(t *testing.T) {
				m := &Manager{
					startedAt: time.Now(),
					current:   state.ProcmonState{Monitors: make(map[string]*state.MonitorRuntimeState)},
				}
				counts := make(map[string]int)
				for i, status := range []string{first.status, second.status} {
					id := strconv.Itoa(i)
					m.cfg.Monitors = append(m.cfg.Monitors, config.MonitorConfig{ID: id})
					m.current.Monitors[id] = &state.MonitorRuntimeState{ID: id, Status: status}
					counts[status]++
				}
				want := first.overall
				if second.rank > first.rank {
					want = second.overall
				}
				got := m.Snapshot()
				if got.OverallStatus != want {
					t.Errorf("overall status = %q, want %q", got.OverallStatus, want)
				}
				if got.MonitorCount != 2 || len(got.Monitors) != 2 ||
					got.HealthyCount != counts["healthy"] ||
					got.DegradedCount != counts["degraded"] ||
					got.RecoveringCount != counts["recovering"]+counts["checking"] ||
					got.FailedCount != counts["failed"] ||
					got.DisabledCount != counts["disabled"] {
					t.Errorf("unexpected snapshot counts: %+v", got)
				}
			})
		}
	}
	m := &Manager{startedAt: time.Now()}
	if got := m.Snapshot(); got.OverallStatus != "healthy" || got.MonitorCount != 0 {
		t.Errorf("empty inventory changed semantics: %+v", got)
	}
}

func TestMonitorLockCancellation(t *testing.T) {
	m := &Manager{}
	lock := m.monitorLock("test")
	lock <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- m.withMonitorLock(ctx, "test", func() { t.Error("canceled callback ran") })
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter stayed blocked")
	}
	<-lock
	if err := m.withMonitorLock(ctx, "test", func() { t.Error("pre-canceled callback ran") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled: %v", err)
	}
	ran := false
	if err := m.withMonitorLock(context.Background(), "test", func() { ran = true }); err != nil || !ran {
		t.Fatalf("lock not reusable: ran=%t error=%v", ran, err)
	}
}

func TestCanceledCheckDoesNotCountFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	m, monitor := testManager(runnerFunc(func(context.Context, string) (command.Outcome, error) {
		calls++
		cancel()
		return command.Outcome{ExitCode: -1}, context.Canceled
	}))
	_, err := m.TriggerCheck(ctx, monitor.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	s := m.current.Monitors[monitor.ID]
	if calls != 1 || s.CheckRunCount != 0 || s.FailureCount != 0 || s.RecoveryCount != 0 || s.Status != "healthy" {
		t.Fatalf("canceled check changed accounting: calls=%d state=%+v", calls, s)
	}
}

func TestCanceledRecoveryDoesNotCountFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, monitor := testManager(runnerFunc(func(_ context.Context, cmd string) (command.Outcome, error) {
		if cmd == "recover" {
			cancel()
			return command.Outcome{ExitCode: -1}, context.Canceled
		}
		return command.Outcome{}, nil
	}))
	_, err := m.TriggerRecovery(ctx, monitor.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	s := m.current.Monitors[monitor.ID]
	if s.RecoveryCount != 0 || s.RecoveryFailureCount != 0 || s.Status != "healthy" || !cooldownActive(s.CooldownUntil, time.Now()) {
		t.Fatalf("canceled recovery accounting/cooldown: %+v", s)
	}
}

func TestRecoveryPersistsCooldownBeforeAction(t *testing.T) {
	var m *Manager
	runner := runnerFunc(func(_ context.Context, cmd string) (command.Outcome, error) {
		if cmd == "recover" {
			saved := m.store.(*countingStateStore).last.Monitors["test"]
			if saved == nil || saved.Status != "recovering" || !cooldownActive(saved.CooldownUntil, time.Now()) {
				t.Fatalf("action started without persisted cooldown: %+v", saved)
			}
		}
		return command.Outcome{}, nil
	})
	m, _ = testManager(runner)
	if _, err := m.TriggerRecovery(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryAbortsOnStartPersistenceFailure(t *testing.T) {
	for _, failState := range []bool{false, true} {
		t.Run(map[bool]string{true: "state", false: "history"}[failState], func(t *testing.T) {
			actions := 0
			m, monitor := testManager(runnerFunc(func(_ context.Context, cmd string) (command.Outcome, error) {
				if cmd == "recover" {
					actions++
				}
				return command.Outcome{}, nil
			}))
			failure := errors.New("disk failure")
			if failState {
				m.store.(*countingStateStore).err = failure
			} else {
				m.eventStore.(*countingEventStore).err = failure
			}
			_, err := m.TriggerRecovery(context.Background(), monitor.ID)
			if !errors.Is(err, failure) {
				t.Fatalf("got %v, want persistence error", err)
			}
			s := m.current.Monitors[monitor.ID]
			if actions != 0 || s.RecoveryCount != 0 || s.RecoveryFailureCount != 0 || s.Status != "healthy" {
				t.Fatalf("aborted recovery ran or counted: actions=%d state=%+v", actions, s)
			}
		})
	}
}

type countingEventStore struct {
	saves int
	err   error
}

func (s *countingEventStore) Load() (events.History, error) {
	return events.History{}, nil
}

func (s *countingEventStore) Save(events.History) error {
	s.saves++
	return s.err
}

func TestPersistSkipsUnchangedHistory(t *testing.T) {
	states := &countingStateStore{}
	history := &countingEventStore{}
	m := &Manager{store: states, eventStore: history, eventsDirty: true}
	for i := 0; i < 3; i++ {
		if err := m.persistLocked(); err != nil {
			t.Fatal(err)
		}
	}
	if states.saves != 3 || history.saves != 1 {
		t.Fatalf("saves: state=%d history=%d; want 3, 1", states.saves, history.saves)
	}
	m.appendEventLocked(events.Event{ID: "new"})
	if err := m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if history.saves != 2 || m.eventsDirty {
		t.Fatalf("new event not saved cleanly: saves=%d dirty=%t", history.saves, m.eventsDirty)
	}
}

func TestPersistRetriesDirtyHistoryAfterFailure(t *testing.T) {
	for _, failState := range []bool{false, true} {
		name := "history"
		if failState {
			name = "state"
		}
		t.Run(name, func(t *testing.T) {
			states := &countingStateStore{}
			history := &countingEventStore{}
			m := &Manager{store: states, eventStore: history}
			m.appendEventLocked(events.Event{ID: "retry"})
			failure := errors.New("save failed")
			if failState {
				states.err = failure
			} else {
				history.err = failure
			}
			if err := m.persistLocked(); !errors.Is(err, failure) {
				t.Fatalf("got %v, want save failure", err)
			}
			if !m.eventsDirty {
				t.Fatal("failed persistence cleared dirty history")
			}
			states.err, history.err = nil, nil
			if err := m.persistLocked(); err != nil {
				t.Fatal(err)
			}
			wantSaves := 2
			if failState {
				wantSaves = 1
			}
			if history.saves != wantSaves || m.eventsDirty {
				t.Fatalf("retry: saves=%d dirty=%t", history.saves, m.eventsDirty)
			}
		})
	}
}
