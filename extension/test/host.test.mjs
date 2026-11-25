// Run with: node --test extension/test
import assert from "node:assert/strict";
import { test } from "node:test";
import { hostFromUrl, normalizeHost } from "../host.js";

test("normalizeHost accepts plain hostnames", () => {
  const good = {
    "youtube.com": "youtube.com",
    "YouTube.COM": "youtube.com",
    "www.youtube.com": "youtube.com",
    "WWW.Example.Co.ID.": "example.co.id",
    "m.youtube.com": "m.youtube.com",
    "xn--bcher-kva.example": "xn--bcher-kva.example",
    "www.www.example.com": "example.com",
  };
  for (const [input, want] of Object.entries(good)) assert.equal(normalizeHost(input), want, input);
});

test("normalizeHost refuses everything else (same list as the Go tests)", () => {
  const bad = [
    "", " ", ".", "localhost", "intranet", "com",
    "https://youtube.com", "youtube.com/watch?v=abc", "youtube.com?x=1", "youtube.com#frag",
    "user:pass@youtube.com", "youtube.com:8080", "[::1]", "::1", "192.168.1.10", "127.0.0.1", "8.8.8.8",
    "bücher.example", "例え.jp", "you tube.com", "-bad.example.com", "bad-.example.com", "a..b.com", ".example.com",
    "exa_mple.com", "a.b1", "a.123", "example.c", "a".repeat(64) + ".com", "a.".repeat(130) + "com",
  ];
  for (const input of bad) assert.equal(normalizeHost(input), "", JSON.stringify(input));
  assert.equal(normalizeHost(undefined), "");
  assert.equal(normalizeHost(null), "");
});

test("hostFromUrl keeps only the host of ordinary web pages", () => {
  const cases = {
    "https://www.youtube.com/watch?v=abc#t=1": "youtube.com",
    "http://m.youtube.com/": "m.youtube.com",
    "https://user:pass@github.com:8443/org/repo?x=1": "github.com",
    "https://GITHUB.com./x": "github.com",
    "https://192.168.1.10/admin": "",
    "https://[::1]:8080/": "",
    "http://localhost:3000/app": "",
    "https://intranet/wiki": "",
    "chrome://settings/": "",
    "chrome-extension://abc/options.html": "",
    "edge://newtab/": "",
    "about:blank": "",
    "file:///C:/secret.txt": "",
    "data:text/html,<h1>x</h1>": "",
    "javascript:alert(1)": "",
    "view-source:https://example.com": "",
    "not a url": "",
    "": "",
    "https://bücher.example/": "xn--bcher-kva.example", // the browser hands over punycode
  };
  for (const [url, want] of Object.entries(cases)) assert.equal(hostFromUrl(url), want, url);
  // whatever the URL, the result never contains a path, query, fragment, credentials or port
  for (const url of ["https://a.com/p?q=1#f", "https://u:p@b.org:9/x", "https://c.net/%2F"]) {
    assert.doesNotMatch(hostFromUrl(url), /[\/?#@:%]/);
  }
});
