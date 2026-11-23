-- Phase 7c: when a month last changed, for the 60 s debounce of the GitHub backup.
alter table worklog_sync add column if not exists changed_at timestamptz not null default now();
