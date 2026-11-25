-- Phase 8c: time per website, from the owner's own browser extension. Only the
-- hostname of the active tab is stored: never a path, query, title or content.

create table if not exists domain_buckets (
    device_id      bigint not null references devices(id),
    bucket_start   timestamptz not null,       -- start of the minute
    domain         text not null check (char_length(domain) between 1 and 253),
    active_seconds int not null check (active_seconds between 0 and 60),
    primary key (device_id, bucket_start, domain)
);
create index if not exists domain_buckets_start on domain_buckets (bucket_start);

-- matched by suffix: 'youtube.com' also covers 'm.youtube.com'
create table if not exists domain_categories (
    domain     text primary key,
    category   text not null check (category in ('work', 'browse', 'distract', 'other')),
    updated_at timestamptz not null default now()
);

-- never recorded
create table if not exists domain_blocklist (domain text primary key);
