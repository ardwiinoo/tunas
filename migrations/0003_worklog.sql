-- Phase 7: worklog (PRD 5.1; numbered 0003 because 0002 is agent status).

create table if not exists worklog_entries (
  id bigserial primary key,
  entry_date date not null,                  -- local date (TIMEZONE)
  position int not null default 0,           -- bullet order within the day
  text text not null check (char_length(text) between 1 and 2000),
  source text not null default 'telegram' check (source in ('telegram','agent','import')),
  client_id text unique,                     -- set by the agent: resending the same entry is a no-op
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index if not exists worklog_entries_date_idx on worklog_entries(entry_date);

create table if not exists holidays (
  day date not null,
  name text not null,
  type text not null check (type in ('holiday','leave')),  -- holiday = libur nasional, leave = cuti bersama
  fetched_at timestamptz not null default now(),
  primary key (day, name)
);

create table if not exists calendar_overrides (  -- the owner's corrections (win over the API)
  day date primary key,
  kind text not null check (kind in ('off','work')),
  note text
);

create table if not exists worklog_prompts (      -- reminder trail; the unique key = idempotency
  id bigserial primary key,
  slot_date date not null,
  slot text not null,                             -- '10:30' | '13:30' | '16:30' | 'closing'
  telegram_message_id bigint,
  status text not null default 'sent' check (status in ('sent','answered','later','skipped','expired')),
  sent_at timestamptz not null default now(),
  answered_at timestamptz,
  answered_via text,                              -- 'telegram' | 'agent'
  unique (slot_date, slot)
);

create table if not exists worklog_sync (         -- GitHub backup state per month
  month text primary key,                         -- '2026-11'
  dirty boolean not null default true,
  last_sha text,
  last_content_hash text,
  last_synced_at timestamptz,
  last_error text,
  status text not null default 'pending' check (status in ('pending','ok','conflict','error'))
);

create table if not exists bot_pending (          -- waiting for a text reply after a button
  chat_id bigint primary key,
  kind text not null,                             -- 'worklog.write' | 'worklog.edit' | 'worklog.add'
  payload jsonb not null default '{}',
  expires_at timestamptz not null
);
