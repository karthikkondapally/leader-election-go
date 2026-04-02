package pgelect_test

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"

	"github.com/pgelect/pgelect"
)

func TestSlogLogger_AllLevels(t *testing.T) {
	var buf bytes.Buffer
	l := pgelect.NewSlogLogger(
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	)
	l.Debug("debug-msg", "k", "v")
	l.Info("info-msg")
	l.Warn("warn-msg", "x", 1)
	l.Error("error-msg")

	out := buf.String()
	for _, want := range []string{"debug-msg", "info-msg", "warn-msg", "error-msg"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in slog output, got:\n%s", want, out)
		}
	}
}

func TestStdLogger_AllLevels(t *testing.T) {
	var buf bytes.Buffer
	l := pgelect.NewStdLogger(log.New(&buf, "", 0))
	l.Debug("dbg", "k", "v")
	l.Info("inf")
	l.Warn("wrn", "a", "b")
	l.Error("err")

	out := buf.String()
	for _, want := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected level %q in std output, got:\n%s", want, out)
		}
	}
}

func TestWriterLogger_ContainsKeyValues(t *testing.T) {
	var buf bytes.Buffer
	l := pgelect.NewWriterLogger(&buf)
	l.Info("hello", "foo", "bar", "num", 42)

	out := buf.String()
	for _, want := range []string{"hello", "foo=bar", "num=42"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in writer output, got:\n%s", want, out)
		}
	}
}

func TestWriterLogger_OddKeyValues(t *testing.T) {
	var buf bytes.Buffer
	l := pgelect.NewWriterLogger(&buf)
	// Odd number of kv args — should not panic.
	l.Warn("warning", "orphan-key")

	out := buf.String()
	if !strings.Contains(out, "orphan-key") {
		t.Errorf("expected orphan-key in output, got:\n%s", out)
	}
	if !strings.Contains(out, "<missing>") {
		t.Errorf("expected <missing> marker for orphan key, got:\n%s", out)
	}
}

func TestNoopLogger_AllMethodsSilent(t *testing.T) {
	l := pgelect.NoopLogger()
	// None of these should panic or write anything.
	l.Debug("d", "k", "v")
	l.Info("i")
	l.Warn("w", "k", 1)
	l.Error("e", "err", "oh no")
}

func TestDefaultLogger_DoesNotPanic(t *testing.T) {
	l := pgelect.NewDefaultLogger()
	// Just verify it doesn't panic; output goes to stderr which is fine in tests.
	l.Info("test", "key", "value")
}
