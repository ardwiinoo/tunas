-- Phase 9b: device tokens expire, the last client address is kept (to warn
-- about a new one) and the two warnings are sent once.

alter table devices add column if not exists expires_at       timestamptz;
alter table devices add column if not exists last_seen_ip     text;
alter table devices add column if not exists ip_alerted_at    timestamptz;   -- last new-address warning (24 h cooldown)
alter table devices add column if not exists expiry_warned_at timestamptz;   -- the one expires-soon warning

-- devices that already exist get the same 180 days, counted from this migration
update devices set expires_at = now() + interval '180 days' where expires_at is null and revoked_at is null;
