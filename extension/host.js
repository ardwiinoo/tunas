// Hostname rules of the Tunas extension. They mirror internal/domains in the
// agent (Go): only a plain DNS hostname ever leaves the browser, never a path,
// query, fragment, credentials, port, IP address or an internal name.

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const TLD = /^([a-z]{2,63}|xn--[a-z0-9-]{1,59})$/;

/** Returns the normalized hostname, or "" when it is not a plain public hostname. */
export function normalizeHost(host) {
  let h = String(host ?? "").trim().toLowerCase();
  if (h.endsWith(".")) h = h.slice(0, -1);
  while (h.startsWith("www.")) h = h.slice(4);
  if (!h || h.length > 253) return "";
  const labels = h.split(".");
  if (labels.length < 2) return "";
  if (!labels.every((l) => LABEL.test(l))) return "";
  if (!TLD.test(labels[labels.length - 1])) return "";
  return h;
}

/**
 * The hostname of a tab URL, or "" for anything that is not an ordinary web
 * page (browser pages, files, extensions, data URLs, IP addresses, localhost).
 */
export function hostFromUrl(url) {
  let u;
  try {
    u = new URL(url);
  } catch {
    return "";
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") return "";
  return normalizeHost(u.hostname);
}
