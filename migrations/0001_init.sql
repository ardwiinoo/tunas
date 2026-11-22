create table if not exists tasks (
  id bigserial primary key,
  title text not null,
  notes text,
  status text not null default 'open' check (status in ('open','done','cancelled')),
  due_at timestamptz,
  created_at timestamptz not null default now(),
  completed_at timestamptz
);

create table if not exists reminders (
  id bigserial primary key,
  task_id bigint references tasks(id) on delete set null,
  text text not null,
  remind_at timestamptz not null,
  recurrence text,
  status text not null default 'pending' check (status in ('pending','sent','cancelled','failed')),
  attempts int not null default 0,
  last_attempt_at timestamptz,
  sent_at timestamptz,
  created_at timestamptz not null default now()
);
create index if not exists reminders_status_remind_at_idx on reminders(status, remind_at);

create table if not exists devices (
  id bigserial primary key,
  name text not null unique,
  token_hash text not null unique,
  last_seen_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz not null default now()
);

create table if not exists activity_buckets (
  device_id bigint not null references devices(id),
  bucket_start timestamptz not null,
  app text not null,
  category text not null check (category in ('work','browse','distract','other')),
  active_seconds int not null,
  idle_seconds int not null,
  primary key (device_id, bucket_start, app)
);

create table if not exists events (
  id bigserial primary key,
  device_id bigint references devices(id),
  type text not null,
  payload jsonb not null default '{}',
  created_at timestamptz not null default now(),
  acked_at timestamptz
);
create index if not exists events_device_id_idx on events(device_id, id) where acked_at is null;

create table if not exists settings (
  key text primary key,
  value jsonb not null
);

create table if not exists llm_usage (
  day date primary key,
  requests int not null default 0
);
