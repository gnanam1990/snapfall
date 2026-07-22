package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// capture returns a logger writing JSON lines into buf.
func capture(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

func lastLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	var m map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &m); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	return m
}

// G1: every log line carries the correlation IDs in scope.
func TestFrom_AttachesCorrelationIDs(t *testing.T) {
	var buf bytes.Buffer
	base := capture(&buf)

	ctx := With(context.Background(), Correlation{Org: "org_demo", Job: "job_104", Task: "task_1"})
	From(ctx, base).Info("hello")

	m := lastLine(t, &buf)
	if m["org_id"] != "org_demo" || m["job_id"] != "job_104" || m["task_id"] != "task_1" {
		t.Errorf("correlation IDs missing from log line: %v", m)
	}
	if _, present := m["intent_id"]; present {
		t.Error("unset IDs must be omitted, not logged empty")
	}
}

// Narrowing scope must merge, not replace: setting Job keeps the Org set upstream.
func TestWith_MergesOverUpstreamScope(t *testing.T) {
	ctx := With(context.Background(), Correlation{Org: "org_demo"})
	ctx = WithJob(ctx, "job_104")
	ctx = With(ctx, Correlation{Intent: "pi_01"})

	c := FromContext(ctx)
	if c.Org != "org_demo" || c.Job != "job_104" || c.Intent != "pi_01" {
		t.Errorf("merge lost fields: %+v", c)
	}
}

// A context with no IDs must return the base logger untouched — no empty attrs.
func TestFrom_NoIDsIsPassthrough(t *testing.T) {
	var buf bytes.Buffer
	base := capture(&buf)

	From(context.Background(), base).Info("bare")

	m := lastLine(t, &buf)
	for _, k := range []string{"org_id", "job_id", "task_id", "intent_id", "advance_id"} {
		if _, present := m[k]; present {
			t.Errorf("%s must not appear on an unscoped line", k)
		}
	}
}
