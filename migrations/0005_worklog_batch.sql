-- Phase 8a: multi-line /log. An entry remembers the paste it came from (so
-- "Batalkan semua" removes only that paste), and a big or dated paste waits
-- in worklog_batches for "Simpan" / "Batal".

alter table worklog_entries add column if not exists batch_id text;
create index if not exists worklog_entries_batch_idx on worklog_entries(batch_id) where batch_id is not null;

create table if not exists worklog_batches (
  id text primary key,
  payload jsonb not null,           -- [{"date":"2026-11-24","text":"..."}]
  expires_at timestamptz not null
);
