-- MonA / ClickHouse schema (high-volume time-series metrics)
-- Keep writes append-only; derive aggregates via materialized views later.

CREATE DATABASE IF NOT EXISTS mona;

CREATE TABLE IF NOT EXISTS mona.asic_metrics (
  ts           DateTime64(3, 'UTC'),
  device_id    UUID,
  ip           String,
  shard_id     LowCardinality(String),

  vendor       LowCardinality(String),
  model        LowCardinality(String),
  firmware     LowCardinality(String),

  online       UInt8,
  hashrate_ths Float64,
  temp_max_c   Float64,
  fan_rpm_max  UInt32,
  power_w      UInt32,
  uptime_s     UInt64,

  -- flexible fields (board temps, chip temps, per-chain hashrate, error codes)
  kv           Map(String, String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (device_id, ts)
TTL ts + INTERVAL 180 DAY DELETE;

-- Optional: pre-aggregates for Grafana fast queries can be added later:
-- - 1m buckets per device
-- - 5m buckets per rack/tag

