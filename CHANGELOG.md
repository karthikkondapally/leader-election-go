# Changelog

All notable changes to pgelect are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

### Added
- Initial public release

---

## [0.1.0] - 2025-01-01

### Added
- Core leader election via PostgreSQL session-scoped advisory locks
- Pinned `*sql.Conn` — safe with PgBouncer in transaction pooling mode
- Three DB input modes: `DB *sql.DB`, `DSN string`, explicit `DBHost/DBPort/...` fields
- `ConfigFromEnv()` — full configuration from `PGELECT_*` environment variables
- `OnElected(leaderCtx)` callback — context cancelled when leadership is lost
- `OnRevoked()` callback — fires after lock is released
- `Leases(ctx)` — observable cluster state from any instance
- `CreateSchema(ctx)` — idempotent schema bootstrap
- Pluggable `Logger` interface with built-in adapters:
  - `NewSlogLogger` — `log/slog` (Go standard library, recommended)
  - `NewStdLogger` — `log.Logger`
  - `NewWriterLogger` — any `io.Writer`
  - `NoopLogger` — discard (default)
- `fxpgelect` sub-package — uber/fx `Module` with OnStart/OnStop lifecycle hooks
- `pgxadapter` sub-package — pgx/v5 pool adapter
- `gormadapter` sub-package — GORM adapter
- Configurable timing: `LeaseDuration`, `RenewInterval`, `RetryInterval`,
  `ReconnectBackoff`, `ShutdownTimeout`, `OnElectedDrainTimeout`
- `AutoCreateSchema` flag for zero-migration startup
- `DBPoolConfig` — pool sizing control when pgelect owns the connection
- GitHub Actions CI: unit tests (Go 1.22 + 1.23), integration tests, golangci-lint
- `schema.sql` — standalone migration file for external migration tools
- `Makefile` — common developer tasks
- 35 unit tests, no database required

[Unreleased]: https://github.com/pgelect/pgelect/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/pgelect/pgelect/releases/tag/v0.1.0
