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
- CI GitHub: -

Angka ukur: container lokal (Postgres + bot tanpa token valid) RSS 8,0 MiB, CPU 0,01%; shutdown bersih 1 detik.

Menunggu pemilik:
- Smoke test produksi (`docs/DEPLOY.md` bagian 9) setelah langkah deploy 2 sampai 6 selesai.
