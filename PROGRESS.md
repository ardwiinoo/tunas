# Progress

## Fase 0: Fondasi repo dan CI/CD [x] (kode selesai; deploy menunggu pemilik)
- [x] `go.mod`, tata letak repo, `Makefile` (`test`, `lint`, `build`, `build-agent`)
- [x] `cmd/server`: `serve` dengan `/healthz`, `healthcheck`, muat konfigurasi, log slog JSON
- [x] `cmd/agent`: stub yang membaca `config.yaml`
- [x] Dockerfile, `docker-compose.yml`, `.env.example`, `caddy-site.conf`
- [x] Workflow `ci.yml`, `deploy.yml`, `release-agent.yml`
- [x] `README.md`, `AGENTS.md`, `PROGRESS.md` (+ DEPLOY/DECISIONS lokal)
- [x] `.gitignore`

Gerbang fase:
- gofmt/vet/test: lulus
- docker build lokal: lulus (image 17,4 MB; `healthcheck` di dalam container exit 0)
- CI GitHub: hijau (CI, Deploy build+push GHCR, Release agent) pada b5ba4f4
- build Windows: lulus

Catatan / keputusan:
- Go 1.27 (1.25 sudah tidak dirawat).
- Deploy otomatis melewati langkah SSH sampai secret `VPS_HOST` diisi.

Angka ukur (memori, CPU): container lokal idle 2,2 MiB / CPU 0% (Fase 0, hanya HTTP).

Menunggu pemilik:
- Langkah deploy 2 sampai 6 (secrets, DB, `/srv/tunas`, DNS, Caddy). Penerimaan `curl https://tunas.ardwiinoo.my.id/healthz` menunggu ini.

## Fase 1: Server MVP (bot, tugas, pengingat) [x] (kode selesai; smoke test produksi menunggu deploy)
- [x] Migrasi `0001_init.sql`, dijalankan otomatis saat start (runner sendiri, advisory lock)
- [x] Bot owner-only (chat pribadi pemilik saja), perintah `/start /help /task /tasks /done /remind /reminders /cancel /ping`
- [x] Parser waktu deterministik + tes tabel (semua format, batas hari/bulan/tahun, lewat tengah malam, WIB dari jam UTC)
- [x] Scheduler pengingat 30 detik, `FOR UPDATE SKIP LOCKED`, retry tiap menit maks 5 lalu `failed` + pemberitahuan, tanda terlambat > 5 menit, berulang, jam palsu di tes
- [x] Subcommand `device create|list|revoke` (token hanya tampil sekali, disimpan sebagai sha256)
- [x] Rate limit `/v1/*` per IP, error tanpa rahasia (token bot disaring dari error Telegram)

Gerbang fase:
- tes unit + integrasi Postgres 17 (lokal): lulus
- tidak ada rahasia di log: lulus (smoke test container dengan token palsu: 0 kemunculan token/password)
- CI GitHub: hijau (CI, Deploy, Release agent) pada 2e38f3b

Angka ukur: container lokal (Postgres + bot tanpa token valid) RSS 8,0 MiB, CPU 0,01%; shutdown bersih 1 detik.

Menunggu pemilik:
- Smoke test produksi (`docs/DEPLOY.md` bagian 9) setelah langkah deploy 2 sampai 6 selesai.

## Fase 2: Agen laptop (pelacak, sinkron, /today) [x] (kode selesai; smoke test produksi menunggu deploy)
- [x] Tracker Windows (`GetForegroundWindow` → PID → `QueryFullProcessImageName`, `GetLastInputInfo`) di balik antarmuka `Sampler` + palsu untuk tes; stub non-Windows
- [x] Buffer SQLite (modernc), flush 60 detik, backoff eksponensial s.d. 15 menit, idempoten (total penuh per bucket, upsert ganti)
- [x] Endpoint `POST /v1/agent/activity`, `GET /v1/agent/events` (long-poll ≤ 25 dtk), `POST /v1/agent/events/ack`, `GET /v1/agent/state`; auth token perangkat, body ≤ 1 MB, validasi batch
- [x] Event `reminder.due`, `task.created`, `task.done` dibuat server dan dikonsumsi agen
- [x] Paket `forms` + tes tabel (prioritas, cooldown tegur, gerakan idle 3 sampai 7 menit); agen mencatat transisi form ke log
- [x] `/today`: total aktif, 5 aplikasi teratas, sesi terpanjang (jeda ≤ 2 menit), per kategori
- [x] Rilis `tunas-agent.exe` lewat CI (artefak; release bila tag `agent-v*`)
- [x] Panduan Task Scheduler + skrip ukur `scripts/measure-agent.ps1` (DEPLOY.md bagian 10)

Gerbang fase:
- tes unit + integrasi: lulus (termasuk skenario offline 5 menit → online: 10 bucket, 600 detik, tanpa hilang/ganda)
- build Windows: lulus (12 MB)
- tidak ada judul jendela di kode/DB/log: lulus (tes `TestNoWindowTitleAPIs`; wire format tanpa field judul; server menolak field tak dikenal)
- ukur agen: diukur di laptop pemilik terhadap server lokal (bukan produksi), 3 menit

Angka ukur (agen, Windows, 12 core): working set rata-rata 13,5 MB (maks 13,6 MB), private bytes 47,7 MB, CPU rata-rata ≈ 0,00% (0,03% satu core). Catatan: peluncuran pertama exe yang baru di-build sempat 52 MB; setelah restart stabil 13,5 MB (lihat DECISIONS).
Uji lokal end-to-end: agen nyata → server lokal → Postgres: bucket `Code.exe` tersinkron, event `reminder.due` diterima (age 12 dtk) dan form berganti `tidur → ingat → tidur`.

Menunggu pemilik:
- Deploy produksi, lalu smoke test DEPLOY.md bagian 10.6 dan pengukuran 10 menit di produksi.

## Fase 3: Kecerdasan (LLM) dan form [x] (kode selesai; menunggu virtual key Bifrost untuk uji nyata)
- [x] Klien `internal/llm` ke Bifrost: timeout 45 dtk, `fallbacks`, retry sekali bila `content` kosong + `finish_reason=length` (max_tokens ×2), log `routing_info`, tanpa loop retry 429; `llm_usage` + batas `LLM_DAILY_REQUEST_CAP` (lewat → balasan templat)
- [x] Pesan biasa → klasifikasi maksud (JSON divalidasi ketat); pengingat/tugas dikonfirmasi tombol Ya/Batal; waktu dihitung & divalidasi kode
- [x] Ringkasan harian 21:00 WIB (bisa diubah di `settings`), persona form aktif, dilewati bila tanpa data, fallback templat
- [x] `/tanya` dengan templat query tetap (`top_apps`, `longest_session`, `total_active`, `by_category` × 5 rentang); tanpa text-to-SQL
- [x] `modes/*.yaml` (embed) untuk 6 form; `/form <id>` dan `/form auto` (setting + event `form.set` ke agen)
- [ ] Virtual key Bifrost khusus Tunas **[PEMILIK]**

Gerbang fase:
- tes dengan klien LLM palsu: lulus
- tidak ada data mentah/judul ke LLM: lulus (`TestAskUsesTemplateAndOnlyAggregates` memeriksa seluruh payload: tanpa timestamp mentah, tanpa field bucket)
- batas harian teruji: lulus (unit `TestCapped` + integrasi Postgres `TestDailyCapWithDB`)

Menunggu pemilik:
- Virtual key Bifrost + env LLM (DEPLOY.md bagian 11), lalu smoke test penerimaan Fase 3.
