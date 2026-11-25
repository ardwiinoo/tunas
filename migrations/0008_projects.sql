-- Phase 8f: time per coding project. Only the project name extracted from an
-- allowed editor's window title and the editor's process name are stored;
-- never a window title or a file name.

create table if not exists project_buckets (
    device_id      bigint not null references devices(id),
    bucket_start   timestamptz not null,      -- start of the minute
    project        text not null check (char_length(project) between 1 and 100),
    editor         text not null,             -- lower-case process name, e.g. 'code.exe'
    active_seconds int not null check (active_seconds between 0 and 60),
    primary key (device_id, bucket_start, project, editor)
);
create index if not exists project_buckets_start on project_buckets (bucket_start);

-- 'client-x' shown as 'Proyek X'; the same alias merges projects (also across editors)
create table if not exists project_aliases (
    project text primary key,                 -- original name, lower-case
    alias   text not null check (char_length(alias) between 1 and 100)
);

-- never recorded
create table if not exists project_blocklist (project text primary key);   -- lower-case
