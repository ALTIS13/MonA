-- MonA / PostgreSQL schema (state, config, audit)
-- Notes:
-- - ClickHouse is used for high-volume time-series metrics.
-- - Postgres stores current state, config, automation rules, audit trail.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Devices: identity + static-ish facts
CREATE TABLE IF NOT EXISTS devices (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  shard_id     TEXT NOT NULL,
  ip           INET NOT NULL,
  mac          MACADDR,
  vendor       TEXT NOT NULL DEFAULT 'unknown',      -- antminer/whatsminer/...
  model        TEXT NOT NULL DEFAULT '',
  firmware     TEXT NOT NULL DEFAULT '',             -- stock/vnish/custom + version
  hostname     TEXT NOT NULL DEFAULT '',
  tags         JSONB NOT NULL DEFAULT '{}'::jsonb,    -- groups/labels/rack/vlan/etc
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS devices_ip_uq ON devices(ip);
CREATE INDEX IF NOT EXISTS devices_mac_idx ON devices(mac);
CREATE INDEX IF NOT EXISTS devices_tags_gin ON devices USING GIN(tags);
CREATE INDEX IF NOT EXISTS devices_shard_idx ON devices(shard_id);

-- Current state (hot path). Updated frequently, kept compact.
CREATE TABLE IF NOT EXISTS device_state_current (
  device_id     UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
  online        BOOLEAN NOT NULL DEFAULT false,
  last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_poll_at  TIMESTAMPTZ,

  hashrate_ths  DOUBLE PRECISION,
  temp_max_c    DOUBLE PRECISION,
  fan_rpm_max   INTEGER,
  power_w       INTEGER,
  uptime_s      BIGINT,

  reboot_count_1h INTEGER NOT NULL DEFAULT 0,
  rebooted_at_last TIMESTAMPTZ,

  errors        JSONB NOT NULL DEFAULT '[]'::jsonb,
  meta          JSONB NOT NULL DEFAULT '{}'::jsonb,

  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS device_state_online_idx ON device_state_current(online);
CREATE INDEX IF NOT EXISTS device_state_seen_idx ON device_state_current(last_seen_at DESC);

-- Audit/event log (low/medium volume): store raw protobuf + extracted keys for search.
CREATE TABLE IF NOT EXISTS events (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  ts          TIMESTAMPTZ NOT NULL DEFAULT now(),
  subject     TEXT NOT NULL,
  shard_id    TEXT,
  device_id   UUID REFERENCES devices(id) ON DELETE SET NULL,
  ip          INET,
  mac         MACADDR,
  payload_pb  BYTEA NOT NULL,
  payload_json JSONB, -- optional: decoded/flattened later for troubleshooting
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS events_ts_idx ON events(ts DESC);
CREATE INDEX IF NOT EXISTS events_subject_ts_idx ON events(subject, ts DESC);
CREATE INDEX IF NOT EXISTS events_device_ts_idx ON events(device_id, ts DESC);

-- Reboots: normalized table for "facts reboots"
CREATE TABLE IF NOT EXISTS device_reboots (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  device_id  UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  ts         TIMESTAMPTZ NOT NULL DEFAULT now(),
  reason     TEXT NOT NULL DEFAULT '',
  source     TEXT NOT NULL DEFAULT '', -- collector/core/automation/mikrotik
  meta       JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS device_reboots_device_ts_idx ON device_reboots(device_id, ts DESC);

-- Credentials: encrypted at rest (envelope encryption, key_id points to Vault/SOPS key version)
-- No plaintext passwords anywhere.
CREATE TABLE IF NOT EXISTS credential_profiles (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  vendor       TEXT NOT NULL,   -- antminer/whatsminer/...
  firmware     TEXT NOT NULL,   -- stock/vnish/custom
  username     TEXT NOT NULL,
  password_enc BYTEA NOT NULL,
  key_id       TEXT NOT NULL,   -- Vault transit key/version or SOPS key ref
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS credential_profiles_uq ON credential_profiles(vendor, firmware, username);

