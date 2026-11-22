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

Angka ukur: container lokal dengan LLM aktif (key palsu) RSS 7,8 MiB, CPU 0%; 0 kebocoran token bot/key di log.

Gerbang fase:
- tes dengan klien LLM palsu: lulus
- tidak ada data mentah/judul ke LLM: lulus (`TestAskUsesTemplateAndOnlyAggregates` memeriksa seluruh payload: tanpa timestamp mentah, tanpa field bucket)
- batas harian teruji: lulus (unit `TestCapped` + integrasi Postgres `TestDailyCapWithDB`)

Menunggu pemilik:
- Virtual key Bifrost + env LLM (DEPLOY.md bagian 11), lalu smoke test penerimaan Fase 3.

## Fase 4: Karakter Tunas di layar (overlay) [x] (diuji di laptop pemilik terhadap server lokal)
- [x] Overlay transparan, tanpa bingkai, selalu di atas. Prototipe Ebitengine dibuat dulu; karena memaksa OpenGL (218 MB saat terlihat) diganti jalur cadangan PRD: Win32 layered window (lihat DECISIONS)
- [x] Bubble bersudut bulat dengan ekor, digambar jalur vektor; teks digambar sendiri dengan font tersemat (Go Regular, BSD-3); ukuran menyesuaikan teks; kata dipatahkan otomatis (maks 7 baris, sisanya "…")
- [x] Karakter prosedural sesuai 11.1 (6 form: mata, mulut, daun, pipi, loncat senang, napas tidur, kedip & goyang daun biasa) di balik antarmuka `Renderer`
- [x] Muncul/hilang: tersembunyi (timer 5 Hz, tanpa menggambar) kecuali ada pemicu (pengingat, tugas selesai, tegur, chat), tampil sesuai `duration` lalu hilang
- [x] Hotkey global `Alt+Space` (`RegisterHotKey`) → input di bubble → Enter → `POST /v1/agent/chat` → balasan di bubble
- [x] Klik tembus di area transparan (alpha per piksel), hanya karakter/bubble yang menerima klik
- [x] Posisi sudut + monitor + margin di `config.yaml`; DPI per monitor
- [x] Endpoint `POST /v1/agent/chat` (klien LLM + persona form aktif)
- [x] Perbaikan: dua bubble sekaligus. Penyebabnya dua proses agen berjalan bersamaan (masing-masing punya overlay sendiri). Agen kini single instance (named mutex `Local\TunasAgent`; proses kedua keluar dengan kode 1). Pengelola bubble jadi state machine eksplisit (idle/menampilkan/input) dengan antrean: pengingat dan balasan yang datang saat ada bubble penting atau input menunggu giliran (maks 4), obrolan form diganti di tempat atau dibuang. Pengingat yang terpotong `Alt+Space` tampil lagi setelah input ditutup
- [x] `--debug` membuka jendela konsol sendiri (build GUI `-H=windowsgui` tidak punya konsol); bila agen berhenti karena error, konsol menunggu Enter
- [x] Status loading di semua jalur chat:
  - Overlay: "Tunas lagi mikir..." (titik beranimasi, daun bergoyang, input dikunci), berganti "Agak lama, sebentar ya..." setelah 8 detik.
  - Pesan gagal spesifik (timeout / tidak ada koneksi / jatah LLM habis / LLM bermasalah) dan kotak input dibuka lagi untuk mencoba ulang; tiap kegagalan dicatat di agent.log.
  - Telegram: "typing" diulang tiap 4 detik; LLM gagal → balasan sesuai sebab (bukan diam).
  - Server: 9 detik per model lalu fallback, total maks 20 detik, model dan durasi dicatat di log.
  - Diuji end-to-end lokal dengan gateway palsu: model utama lambat → fallback menjawab dalam 11 detik; semua lambat → "Server lama menjawab" setelah 18 detik; server mati → "Tidak bisa hubungi server"; batas harian → "Jatah LLM hari ini habis".
- [x] Perbaikan fallback LLM: 403 dari gateway tidak lagi menghentikan rantai; semua kegagalan per model lanjut ke model berikutnya. Log per percobaan (model, status). Pesan dibedakan: model diblokir (konfigurasi, plus peringatan ke pemilik), kuota provider habis, provider down, timeout. Diuji dengan gateway palsu: primary 403 → fallback 1 429 → fallback 2 menjawab.
- [x] Status agen:
  - Heartbeat tiap 60 detik (`POST /v1/agent/heartbeat`): versi agen dan status jeda disimpan per perangkat (migrasi 0002).
  - `/status`: online/offline sejak kapan, versi, status jeda, event tertunda. `/settings notif_status on|off`.
  - Kabar Telegram: offline > 15 menit dan kembali online, tiap jenis maks sekali per jam; tidak ada kabar online tanpa kabar offline sebelumnya.
  - Ikon tray: hijau/kuning/merah, menu Jeda pelacakan / Buka log / Keluar.
  - Menyambung ulang setelah sleep/offline: cek `GET /state` dulu, jadi hijau langsung (tidak menunggu long-poll 25 detik). Jumlah event tertunda dan lama offline dicatat. Pengingat > 2 menit tampil dengan "(tertunda N menit)" sesuai urutan; > 6 jam dilewati dan dicatat.
  - Diuji langsung di Windows: ikon tray terdaftar; menu tampil dan "Jeda pelacakan" sampai ke server; server mati → kuning lalu merah dalam 15 detik; server hidup → hijau, `pending_events=2 offline_for=35s`, "minum obat (tertunda 30 menit)" tampil, pengingat 7 jam dilewati.

Gerbang fase:
- tes tata letak bubble / pemecah teks / presenter / render semua form: lulus (bebas platform, jalan di CI Linux)
- build Windows: lulus (12,5 MB)
- ukur memori tersembunyi & terlihat: diukur agent di laptop pemilik (server lokal)

Angka ukur (agen + overlay, Windows, 12 core, layar 100%):
- tersembunyi: working set 24,6 MB, CPU 0,01% semua core (0,17% satu core)
- terlihat (mode input, animasi 15 fps): working set 24,8 sampai 26,2 MB, CPU 0,68% semua core (8,2% satu core)
- uji end-to-end lokal: event `reminder.due` → bubble pengingat tampil; hotkey (disimulasikan via WM_HOTKEY) → "halo tunas" + Enter → balasan server tampil; `WindowFromPoint`: sudut transparan tembus, badan karakter menangkap klik.

Menunggu pemilik:
- Smoke test di laptop dengan server produksi (DEPLOY.md bagian 12), termasuk tekan `Alt+Space` sungguhan dan layar ber-DPI > 100%.

## Fase 5: Kemampuan tambahan dan operasional [x] (kode selesai; menunggu kunci Tavily + cron backup di VPS)
- [x] Pencarian web: riset & keputusan di DECISIONS (Tavily, dipilih pemilik); `/cari` dan "cari ..." di overlay; jawaban + sumber dari kode; jalur tanpa tool calling
- [x] Backup: `scripts/backup-db.sh` (pg_dump harian, simpan 7) + instruksi cron **[PEMILIK]**; `scripts/restore-db.sh` (restore ke DB baru, menolak menimpa); restore terbukti di DB uji lokal (jumlah baris & total detik identik)
- [x] Pemantauan: `/healthz` memeriksa DB (503 bila mati); peringatan Telegram agen tidak terlihat > 24 jam (sekali per kejadian + pemulihan) dan LLM gagal ≥ 3 kali beruntun (cooldown 1 jam)
- [ ] Aksi jarak jauh (opsional): sengaja tidak dikerjakan (lihat DECISIONS)
- [x] Penyempurnaan: zona waktu/DST perangkat (sudah aman by design, dicatat), pembersihan `activity_buckets` > 180 hari (+ event ter-ack > 30 hari), `/export` (JSON.gz via Telegram), dokumentasi akhir (README, ARCHITECTURE lokal)

Gerbang fase:
- tes: lulus (14 paket, termasuk integrasi Postgres)
- restore backup terbukti: lulus (lokal, Postgres 17)
- anggaran 9: server RSS lokal ≤ 8 MiB; agen + overlay 25 MB tersembunyi / 26 MB terlihat

Menunggu pemilik:
- `TAVILY_API_KEY` (DEPLOY.md bagian 14), cron backup + uji restore di VPS (bagian 13), dan semua langkah deploy sebelumnya.
