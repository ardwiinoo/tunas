-- Phase 8b: the app → category mapping lives on the server and is edited from
-- Telegram (/apps, /kategori). The agent's YAML only seeds it. Reports join
-- this table, so a change applies to past days too.

create table if not exists app_categories (
    app        text primary key,   -- lower-case process name, e.g. 'antigravity.exe'
    category   text not null check (category in ('work', 'browse', 'distract', 'other')),
    updated_at timestamptz not null default now()
);
