-- Phase 8g: resource statistics. The agent's latest numbers ride on the
-- heartbeat; every 5 minutes a sample is kept for the 24 h peak and trend.
-- Numbers only, no free text. Retention 7 days (the daily cleanup).

alter table devices add column if not exists last_stats jsonb;

create table if not exists stats_samples (
  ts timestamptz not null,
  scope text not null check (scope in ('server','agent','host')),
  device_id bigint references devices(id),    -- only for scope 'agent'
  data jsonb not null
);
-- PRD 5.4 names (ts, scope, coalesce(device_id, 0)) as the key; an expression
-- cannot be part of a primary key, so it is a unique index.
create unique index if not exists stats_samples_key on stats_samples (ts, scope, coalesce(device_id, 0));
create index if not exists stats_samples_scope_ts on stats_samples (scope, ts desc);
