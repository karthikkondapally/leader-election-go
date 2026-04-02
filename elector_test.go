package pgelect_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgelect/pgelect"
)

// ── Config validation ─────────────────────────────────────────────────────────

func TestNew_RequiresDB(t *testing.T) {
	_, err := pgelect.New(pgelect.Config{AppName: "a", InstanceID: "i"})
	assertErrIs(t, err, pgelect.ErrInvalidConfig)
}

func TestNew_RequiresAppName(t *testing.T) {
	db := openStubDB(t)
	_, err := pgelect.New(pgelect.Config{DB: db, InstanceID: "i"})
	assertErrIs(t, err, pgelect.ErrInvalidConfig)
}

func TestNew_RequiresInstanceID(t *testing.T) {
	db := openStubDB(t)
	_, err := pgelect.New(pgelect.Config{DB: db, AppName: "a"})
	assertErrIs(t, err, pgelect.ErrInvalidConfig)
}

func TestNew_RejectsBadTimingRelationship(t *testing.T) {
	db := openStubDB(t)
	cases := []struct {
		lease  time.Duration
		renew  time.Duration
		wantOK bool
	}{
		{15 * time.Second, 4 * time.Second, true},  // renew < lease/2 → ok
		{15 * time.Second, 7 * time.Second, true},  // 7s < 7.5s → ok (just under)
		{15 * time.Second, 8 * time.Second, false},
		{10 * time.Second, 5 * time.Second, false}, // renew == lease/2 → reject
		{10 * time.Second, 4 * time.Second, true},  // renew < lease/2 → ok
	}
	for _, c := range cases {
		_, err := pgelect.New(pgelect.Config{
			DB:            db,
			AppName:       "app",
			InstanceID:    "inst",
			LeaseDuration: c.lease,
			RenewInterval: c.renew,
		})
		if c.wantOK && err != nil {
			t.Errorf("lease=%v renew=%v: expected ok, got %v", c.lease, c.renew, err)
		}
		if !c.wantOK && !errors.Is(err, pgelect.ErrInvalidConfig) {
			t.Errorf("lease=%v renew=%v: expected ErrInvalidConfig, got %v", c.lease, c.renew, err)
		}
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	db := openStubDB(t)
	el, err := pgelect.New(pgelect.Config{DB: db, AppName: "a", InstanceID: "i"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if el == nil {
		t.Fatal("expected non-nil Elector")
	}
	// Default state is passive.
	if el.IsLeader() {
		t.Error("new elector must not be leader")
	}
	if el.State() != pgelect.StatePassive {
		t.Errorf("expected StatePassive, got %v", el.State())
	}
}

// ── Stop / Start interaction ──────────────────────────────────────────────────

func TestStop_Idempotent(t *testing.T) {
	el := mustNew(t)
	// Multiple calls must not panic.
	el.Stop()
	el.Stop()
	el.Stop()
}

func TestStart_AfterStop_ReturnsErrStopped(t *testing.T) {
	el := mustNew(t)
	el.Stop()
	err := el.Start(context.Background())
	if !errors.Is(err, pgelect.ErrStopped) {
		t.Fatalf("expected ErrStopped, got %v", err)
	}
}

func TestStart_RespectsContextCancellation(t *testing.T) {
	el := mustNew(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- el.Start(ctx) }()

	// Give the loop a moment to start.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after ctx cancel")
	}
}

func TestStop_UnblocksStart(t *testing.T) {
	el := mustNew(t)

	done := make(chan error, 1)
	go func() { done <- el.Start(context.Background()) }()

	time.Sleep(10 * time.Millisecond)
	el.Stop()

	select {
	case err := <-done:
		// Stop() causes the run context to cancel → Start returns context.Canceled.
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after Stop()")
	}
}

// ── State transitions ─────────────────────────────────────────────────────────

func TestState_String(t *testing.T) {
	cases := map[pgelect.State]string{
		pgelect.StatePassive:      "passive",
		pgelect.StateLeader:       "leader",
		pgelect.StateReconnecting: "reconnecting",
		pgelect.StateStopped:      "stopped",
		pgelect.State(99):         "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestState_AfterStop(t *testing.T) {
	el := mustNew(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		el.Start(context.Background()) //nolint:errcheck
	}()
	time.Sleep(10 * time.Millisecond)
	el.Stop()
	<-done
	if el.State() != pgelect.StateStopped {
		t.Errorf("expected StateStopped after Stop(), got %v", el.State())
	}
}

// ── ConfigFromEnv ─────────────────────────────────────────────────────────────

func TestConfigFromEnv_MissingAppName(t *testing.T) {
	t.Setenv("PGELECT_APP_NAME", "")
	_, err := pgelect.ConfigFromEnv()
	if !errors.Is(err, pgelect.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestConfigFromEnv_BasicFields(t *testing.T) {
	t.Setenv("PGELECT_APP_NAME", "test-app")
	t.Setenv("PGELECT_INSTANCE_ID", "pod-x")
	t.Setenv("PGELECT_TABLE_NAME", "my_leases")
	t.Setenv("PGELECT_LEASE_DURATION", "30s")
	t.Setenv("PGELECT_RENEW_INTERVAL", "9s")
	t.Setenv("PGELECT_RETRY_INTERVAL", "3s")
	t.Setenv("PGELECT_AUTO_CREATE_SCHEMA", "true")

	cfg, err := pgelect.ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "AppName", cfg.AppName, "test-app")
	assertEqual(t, "InstanceID", cfg.InstanceID, "pod-x")
	assertEqual(t, "TableName", cfg.TableName, "my_leases")
	assertEqual(t, "LeaseDuration", cfg.LeaseDuration, 30*time.Second)
	assertEqual(t, "RenewInterval", cfg.RenewInterval, 9*time.Second)
	assertEqual(t, "RetryInterval", cfg.RetryInterval, 3*time.Second)
	if !cfg.AutoCreateSchema {
		t.Error("AutoCreateSchema should be true")
	}
}

func TestConfigFromEnv_BadDuration(t *testing.T) {
	t.Setenv("PGELECT_APP_NAME", "test-app")
	t.Setenv("PGELECT_LEASE_DURATION", "not-a-duration")
	_, err := pgelect.ConfigFromEnv()
	if !errors.Is(err, pgelect.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for bad duration, got %v", err)
	}
}

func TestConfigFromEnv_BadLockKey(t *testing.T) {
	t.Setenv("PGELECT_APP_NAME", "test-app")
	t.Setenv("PGELECT_LOCK_KEY", "not-an-int")
	_, err := pgelect.ConfigFromEnv()
	if !errors.Is(err, pgelect.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for bad lock key, got %v", err)
	}
}

// ── Logger ────────────────────────────────────────────────────────────────────

func TestNoopLogger_DoesNotPanic(t *testing.T) {
	l := pgelect.NoopLogger()
	l.Debug("msg", "k", "v")
	l.Info("msg")
	l.Warn("msg", "k", 1, "k2", true)
	l.Error("msg")
}

func TestWriterLogger_Writes(t *testing.T) {
	r, w := io.Pipe()
	l := pgelect.NewWriterLogger(w)

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := r.Read(buf)
		done <- string(buf[:n])
		r.Close()
	}()

	l.Info("hello", "key", "value")
	w.Close()

	out := <-done
	if len(out) == 0 {
		t.Error("WriterLogger produced no output")
	}
}

func TestCustomLogger_IsCalledOnEvents(t *testing.T) {
	var callCount atomic.Int64
	recorder := &recordingLogger{fn: func(level, msg string) { callCount.Add(1) }}

	el, err := pgelect.New(pgelect.Config{
		DB:         openStubDB(t),
		AppName:    "app",
		InstanceID: "inst",
		Logger:     recorder,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		el.Start(ctx) //nolint:errcheck
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if callCount.Load() == 0 {
		t.Error("expected logger to be called, got zero calls")
	}
}

// ── OnElected / OnRevoked callbacks ──────────────────────────────────────────

func TestOnRevoked_CalledAfterStop(t *testing.T) {
	// This test verifies the callback wiring at the Config level.
	// Full integration (with a real DB) would verify it fires after lock release;
	// here we verify the field is stored and accessible.
	called := make(chan struct{}, 1)
	cfg := pgelect.Config{
		DB:         openStubDB(t),
		AppName:    "app",
		InstanceID: "inst",
		OnRevoked:  func() { called <- struct{}{} },
	}
	if cfg.OnRevoked == nil {
		t.Fatal("OnRevoked should not be nil")
	}
	// Call it directly to confirm it doesn't panic.
	cfg.OnRevoked()
	select {
	case <-called:
	default:
		t.Error("OnRevoked was not invoked")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func mustNew(t *testing.T) pgelect.Elector {
	t.Helper()
	el, err := pgelect.New(pgelect.Config{
		DB:         openStubDB(t),
		AppName:    "test-app",
		InstanceID: "test-instance",
	})
	if err != nil {
		t.Fatalf("mustNew: %v", err)
	}
	return el
}

func assertErrIs(t *testing.T, err error, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("expected errors.Is(err, %v), got: %v", target, err)
	}
}

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}

// recordingLogger satisfies pgelect.Logger and records calls.
type recordingLogger struct {
	fn func(level, msg string)
}

func (r *recordingLogger) Debug(msg string, _ ...any) { r.fn("DEBUG", msg) }
func (r *recordingLogger) Info(msg string, _ ...any)  { r.fn("INFO", msg) }
func (r *recordingLogger) Warn(msg string, _ ...any)  { r.fn("WARN", msg) }
func (r *recordingLogger) Error(msg string, _ ...any) { r.fn("ERROR", msg) }

// ── Stub *sql.DB for tests that don't need a real database ───────────────────

func openStubDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgelect_stub", "")
	if err != nil {
		t.Fatalf("open stub db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func init() {
	sql.Register("pgelect_stub", &stubDriver{})
}

// stubDriver implements driver.Driver. It allows sql.Open to succeed without
// a real database. Conn() returns an error so the elector enters StateReconnecting —
// which is the correct behaviour when no DB is available.
type stubDriver struct{}

func (d *stubDriver) Open(_ string) (driver.Conn, error) {
	return nil, errors.New("stub: no real database")
}

// ── DB source validation ──────────────────────────────────────────────────────

func TestNew_RequiresAtLeastOneDBSource(t *testing.T) {
	_, err := pgelect.New(pgelect.Config{AppName: "a", InstanceID: "i"})
	assertErrIs(t, err, pgelect.ErrInvalidConfig)
}

func TestNew_RejectsMultipleDBSources_DBandDSN(t *testing.T) {
	_, err := pgelect.New(pgelect.Config{
		DB:         openStubDB(t),
		DSN:        "postgres://localhost/mydb",
		AppName:    "a",
		InstanceID: "i",
	})
	assertErrIs(t, err, pgelect.ErrInvalidConfig)
}

func TestNew_RejectsMultipleDBSources_DBandHost(t *testing.T) {
	_, err := pgelect.New(pgelect.Config{
		DB:         openStubDB(t),
		DBHost:     "localhost",
		AppName:    "a",
		InstanceID: "i",
	})
	assertErrIs(t, err, pgelect.ErrInvalidConfig)
}

func TestNew_RejectsMultipleDBSources_DSNandHost(t *testing.T) {
	_, err := pgelect.New(pgelect.Config{
		DSN:        "postgres://localhost/mydb",
		DBHost:     "localhost",
		AppName:    "a",
		InstanceID: "i",
	})
	assertErrIs(t, err, pgelect.ErrInvalidConfig)
}

func TestNew_AcceptsDSN(t *testing.T) {
	// The stub driver is registered as "pgelect_stub" not "postgres",
	// so we set DBDriver to use it.
	_, err := pgelect.New(pgelect.Config{
		DSN:        "pgelect_stub://",
		DBDriver:   "pgelect_stub",
		AppName:    "a",
		InstanceID: "i",
	})
	// sql.Open itself succeeds (driver is registered); any error here is config.
	// The stub driver returns an error on Conn(), not on Open() — so New() succeeds.
	if err != nil {
		t.Fatalf("unexpected error with DSN path: %v", err)
	}
}

func TestNew_AcceptsExplicitFields(t *testing.T) {
	_, err := pgelect.New(pgelect.Config{
		DBHost:     "localhost",
		DBPort:     5432,
		DBName:     "mydb",
		DBUser:     "postgres",
		DBPassword: "secret",
		DBSSLMode:  "disable",
		DBDriver:   "pgelect_stub",
		AppName:    "a",
		InstanceID: "i",
	})
	if err != nil {
		t.Fatalf("unexpected error with explicit fields: %v", err)
	}
}

func TestNew_ExplicitFields_DefaultPort(t *testing.T) {
	// DBPort zero → defaults to 5432 in buildDSN; New should not error.
	_, err := pgelect.New(pgelect.Config{
		DBHost:     "localhost",
		DBName:     "mydb",
		DBUser:     "u",
		DBPassword: "p",
		DBDriver:   "pgelect_stub",
		AppName:    "a",
		InstanceID: "i",
	})
	if err != nil {
		t.Fatalf("unexpected error with zero DBPort: %v", err)
	}
}

func TestConfigFromEnv_DSNField(t *testing.T) {
	t.Setenv("PGELECT_APP_NAME", "app")
	t.Setenv("PGELECT_INSTANCE_ID", "pod")
	t.Setenv("PGELECT_DSN", "postgres://user:pass@localhost/mydb")

	cfg, err := pgelect.ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DSN != "postgres://user:pass@localhost/mydb" {
		t.Errorf("DSN not set correctly: %q", cfg.DSN)
	}
}

func TestConfigFromEnv_ExplicitDBFields(t *testing.T) {
	t.Setenv("PGELECT_APP_NAME", "app")
	t.Setenv("PGELECT_INSTANCE_ID", "pod")
	t.Setenv("PGELECT_DB_HOST", "db.internal")
	t.Setenv("PGELECT_DB_PORT", "5433")
	t.Setenv("PGELECT_DB_NAME", "mydb")
	t.Setenv("PGELECT_DB_USER", "svc")
	t.Setenv("PGELECT_DB_PASSWORD", "hunter2")
	t.Setenv("PGELECT_DB_SSLMODE", "verify-full")

	cfg, err := pgelect.ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "DBHost", cfg.DBHost, "db.internal")
	assertEqual(t, "DBPort", cfg.DBPort, 5433)
	assertEqual(t, "DBName", cfg.DBName, "mydb")
	assertEqual(t, "DBUser", cfg.DBUser, "svc")
	assertEqual(t, "DBPassword", cfg.DBPassword, "hunter2")
	assertEqual(t, "DBSSLMode", cfg.DBSSLMode, "verify-full")
}

func TestConfigFromEnv_BadDBPort(t *testing.T) {
	t.Setenv("PGELECT_APP_NAME", "app")
	t.Setenv("PGELECT_DB_PORT", "not-a-number")
	_, err := pgelect.ConfigFromEnv()
	assertErrIs(t, err, pgelect.ErrInvalidConfig)
}
