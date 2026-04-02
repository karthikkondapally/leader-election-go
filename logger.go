package pgelect

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
)

// Logger is the structured logging interface used by pgelect internally.
// Each method takes a human-readable message and zero or more key-value pairs
// (alternating string key, any value) — the same convention as log/slog and
// uber/zap SugaredLogger.
//
// Implement this interface to use any logging library:
//
//	// zap example
//	type ZapLogger struct{ s *zap.SugaredLogger }
//	func (z ZapLogger) Debug(msg string, kv ...any) { z.s.Debugw(msg, kv...) }
//	func (z ZapLogger) Info(msg string, kv ...any)  { z.s.Infow(msg, kv...)  }
//	func (z ZapLogger) Warn(msg string, kv ...any)  { z.s.Warnw(msg, kv...)  }
//	func (z ZapLogger) Error(msg string, kv ...any) { z.s.Errorw(msg, kv...) }
//
//	// logrus example
//	type LogrusLogger struct{ l *logrus.Logger }
//	func (r LogrusLogger) Debug(msg string, kv ...any) { r.l.WithFields(kvToFields(kv)).Debug(msg) }
//	func (r LogrusLogger) Info(msg string, kv ...any)  { r.l.WithFields(kvToFields(kv)).Info(msg)  }
//	func (r LogrusLogger) Warn(msg string, kv ...any)  { r.l.WithFields(kvToFields(kv)).Warn(msg)  }
//	func (r LogrusLogger) Error(msg string, kv ...any) { r.l.WithFields(kvToFields(kv)).Error(msg) }
type Logger interface {
	Debug(msg string, keysAndValues ...any)
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

// ── Built-in adapters ─────────────────────────────────────────────────────────

// NoopLogger discards all output. Default when Logger is nil.
func NoopLogger() Logger { return noopLogger{} }

type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}
func (noopLogger) Info(string, ...any)  {}
func (noopLogger) Warn(string, ...any)  {}
func (noopLogger) Error(string, ...any) {}

// NewSlogLogger wraps a *log/slog.Logger (standard library, Go 1.21+).
// Recommended for new projects.
//
//	cfg.Logger = pgelect.NewSlogLogger(slog.Default())
func NewSlogLogger(l *slog.Logger) Logger { return &slogAdapter{l: l} }

type slogAdapter struct{ l *slog.Logger }

func (a *slogAdapter) Debug(msg string, kv ...any) { a.l.Debug(msg, kv...) }
func (a *slogAdapter) Info(msg string, kv ...any)  { a.l.Info(msg, kv...) }
func (a *slogAdapter) Warn(msg string, kv ...any)  { a.l.Warn(msg, kv...) }
func (a *slogAdapter) Error(msg string, kv ...any) { a.l.Error(msg, kv...) }

// NewStdLogger wraps a *log.Logger (standard library).
// Key-value pairs are appended as "key=value" strings.
//
//	cfg.Logger = pgelect.NewStdLogger(log.Default())
func NewStdLogger(l *log.Logger) Logger { return &stdAdapter{l: l} }

type stdAdapter struct{ l *log.Logger }

func (a *stdAdapter) Debug(msg string, kv ...any) { a.l.Println(fmtKV("DEBUG", msg, kv)) }
func (a *stdAdapter) Info(msg string, kv ...any)  { a.l.Println(fmtKV("INFO", msg, kv)) }
func (a *stdAdapter) Warn(msg string, kv ...any)  { a.l.Println(fmtKV("WARN", msg, kv)) }
func (a *stdAdapter) Error(msg string, kv ...any) { a.l.Println(fmtKV("ERROR", msg, kv)) }

// NewWriterLogger writes structured text lines to any io.Writer.
//
//	cfg.Logger = pgelect.NewWriterLogger(os.Stderr)
func NewWriterLogger(w io.Writer) Logger {
	return &stdAdapter{l: log.New(w, "", log.LstdFlags)}
}

// NewDefaultLogger writes to stderr with LstdFlags. Useful for quick local runs.
func NewDefaultLogger() Logger { return NewWriterLogger(os.Stderr) }

// fmtKV formats "LEVEL msg key1=val1 key2=val2 ..."
func fmtKV(level, msg string, kv []any) string {
	s := level + " " + msg
	for i := 0; i+1 < len(kv); i += 2 {
		s += fmt.Sprintf(" %v=%v", kv[i], kv[i+1])
	}
	if len(kv)%2 != 0 {
		s += fmt.Sprintf(" %v=<missing>", kv[len(kv)-1])
	}
	return s
}
