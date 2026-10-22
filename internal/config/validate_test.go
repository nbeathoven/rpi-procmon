package config

import "testing"

func TestUnsafeDurationsRejected(t *testing.T) {
	for _, tc := range []struct {
		value string
		zero  bool
		valid bool
	}{{"0s", false, false}, {"-1s", false, false}, {"1s", false, true}, {"0s", true, true}, {"-1s", true, false}, {"bad", true, false}} {
		if err := validateDuration(tc.value, tc.zero); (err == nil) != tc.valid {
			t.Fatalf("duration %q allowZero=%v: %v", tc.value, tc.zero, err)
		}
	}
}

func TestInvalidCheckConfiguration(t *testing.T) {
	for _, check := range []CheckConfig{
		{Type: "http_json", URL: "http://localhost/health", Timeout: "0s"},
		{Type: "docker_log_pattern", Container: "test", Patterns: []string{"["}},
		{Type: "docker_log_pattern", Container: "test", Patterns: []string{"error"}, SuccessPatterns: []string{"["}},
		{Type: "docker_log_pattern", Container: "test", Patterns: []string{"error"}, Since: "-1m"},
		{Type: "command", Command: "true", ForbiddenOutputPatterns: []string{"["}},
	} {
		if err := validateCheck("test", check); err == nil {
			t.Fatalf("accepted invalid check: %+v", check)
		}
	}
	if err := validateTarget("test", TargetConfig{Transport: "ssh", Host: "test", Port: 65536}); err == nil {
		t.Fatal("accepted invalid SSH port")
	}
}

func TestNormalizeRejectsZeroInterval(t *testing.T) {
	cfg := Config{Monitors: []MonitorConfig{{ID: "test", Interval: "0s", Checks: []CheckConfig{{Type: "command", Command: "true"}}, Recovery: []ActionConfig{{Type: "recheck"}}}}}
	if err := Normalize(&cfg); err == nil {
		t.Fatal("zero polling interval must fail before scheduler starts")
	}
}
