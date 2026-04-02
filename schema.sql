-- pgelect schema
-- Add this to your migration tool (flyway, goose, migrate, atlas, etc.)
-- or call Elector.CreateSchema(ctx) on startup — it is fully idempotent.
--
-- Tested on PostgreSQL 13+.

CREATE TABLE IF NOT EXISTS leader_leases (
    -- Identifies the election group.
    -- Multiple apps share one table without interfering.
    app_name    TEXT        NOT NULL,

    -- Uniquely identifies one process instance within the group.
    -- Examples: Kubernetes pod name, hostname+pid, UUID.
    instance_id TEXT        NOT NULL,

    -- 'active'  = this instance currently holds the advisory lock and is leader.
    -- 'passive' = this instance is alive but not the leader.
    status      TEXT        NOT NULL CHECK (status IN ('active', 'passive')),

    -- Updated every RenewInterval by the leader.
    -- Passive nodes watch this column:
    --   if now() - last_seen > LeaseDuration → leader is dead → attempt takeover
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (app_name, instance_id)
);

-- Speeds up "find the active leader for app X" queries.
CREATE INDEX IF NOT EXISTS idx_leader_leases_app_status
    ON leader_leases (app_name, status);

-- ── Optional hygiene ──────────────────────────────────────────────────────────
-- Remove stale passive rows that haven't been seen in over an hour.
-- Prevents table bloat in large fleets where instances churn frequently.
-- Schedule via pg_cron, a periodic job, or your migration runner.
--
-- DELETE FROM leader_leases
-- WHERE  status    = 'passive'
--   AND  last_seen < now() - interval '1 hour';
