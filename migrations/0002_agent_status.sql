-- Agent heartbeat details shown by /status.
alter table devices add column if not exists agent_version text not null default '';
alter table devices add column if not exists tracking_paused boolean not null default false;
alter table devices add column if not exists heartbeat_at timestamptz;
