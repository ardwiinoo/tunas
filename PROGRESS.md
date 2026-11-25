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
- [x] Bubble memakai pipeline Telegram: klasifikasi maksud, templat query tetap untuk pertanyaan data (angka sama dengan `/today`), konfirmasi pengingat/tugas di bubble (Enter = Ya, Esc = Batal, `POST /v1/agent/chat/confirm`), guardrail obrolan, dan penolakan jawaban model yang memuat angka di luar FAKTA.
- [x] Form tampilan "mikir"/"sedih" dan loading lucu:
  - Alur: mikir (melirik ke atas-kanan, mulut "o", daun baling-baling) → mikir lama (keringat, badan bergoyang) → sangat lama (daun terkulai, mata "x" bergantian, "Esc = batal") → berhasil (loncat 0,4 dtk lalu jawaban diketik 30 karakter/dtk, Enter melewati) atau gagal (pose sedih singkat, teks sesuai penyebab, input dibuka lagi).
  - Esc membatalkan context HTTP; hasil permintaan lama dibuang lewat id permintaan.
  - Teks lucu ada di `modes/mikir.yaml` dan bisa ditimpa lewat `%APPDATA%\tunas\modes\mikir.yaml` tanpa kompilasi ulang.
  - Config baru: `loading.slow_after_seconds` (3), `loading.very_slow_after_seconds` (8), `animations` (true).
  - Diuji langsung: tiap fase tertangkap layar; Esc membatalkan sampai ke gateway (`cancelled by client`) dan jawaban yang telat tidak muncul; server mati → pose sedih + "tidak bisa hubungi server".
  - Ukur ulang (Windows, 12 core): working set 28,7 MB; tersembunyi CPU 0,00–0,02% semua core; saat animasi mikir/ketik ±1% semua core (hanya selama bubble terlihat).
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

## Fase 5b: Penyempurnaan dari pemakaian nyata [x] (kode selesai; penerimaan di produksi menunggu deploy)
Sebagian besar butir dikerjakan lebih dulu atas permintaan langsung (PR #2 sampai #7). Sisanya di branch `phase-5b-polish`.
- [x] Rantai fallback LLM: kegagalan apa pun per model lanjut ke model berikutnya; 9 dtk per model, total 20 dtk; log tiap percobaan; pesan dibedakan (diblokir/kuota/provider down/timeout); peringatan Telegram untuk model diblokir (cooldown 1 jam). (PR #3, #5)
- [x] Bubble memakai pipeline Telegram: klasifikasi maksud, templat query tetap, konfirmasi Enter/Esc, guardrail, penolakan angka karangan. (PR #6)
- [x] Konteks percakapan: 6 pesan terakhir dikirim bersama pesan bubble (`history`), direset bila bubble tertutup > 5 menit; dipakai di jalur obrolan.
- [x] Bubble:
  - Setelah jawaban, kotak input muncul di bawahnya (lanjut ngobrol).
  - `Esc` menutup; `Alt+Space` saat bubble terbuka hanya memfokuskan input (tidak lagi menutup).
  - Tutup otomatis setelah `bubble.idle_close_seconds` (default 60) tanpa aktivitas.
  - Satu bubble aktif.
  - Teks panjang tidak lagi dipotong "…": bisa di-scroll dengan ↑/↓, PageUp/PageDown, atau roda mouse, dengan penanda ↑/↓ di tepi kanan.
- [x] State loading (form `mikir`) dengan pembatalan lewat `context`. Pembatalan oleh pemilik dicatat INFO di agen dan server (bukan WARN). (PR #7 + branch ini)
- [x] Visibilitas agen: heartbeat, `/status`, kabar offline/online, sambung ulang otomatis, event tertunda berurutan. (PR #4)
- [x] Agen satu instans, `--debug` membuka konsol, kegagalan chat/long-poll tercatat tanpa token. (PR #2, #3)

Gerbang fase:
- tes klien LLM palsu (403 → 429 → sukses): lulus
- tes state machine bubble dengan jam palsu (idle → input → mikir → lama → sangat lama → berhasil → input → mikir → gagal → input → tutup): lulus
- tes pipeline chat (cocok dengan `/today`, angka karangan ditolak, "ingatkan aku..." membuat baris setelah konfirmasi): lulus
- ukur memori agen: working set 28,7 MB, CPU tersembunyi ≈ 0% (diukur saat PR #7)

Menunggu pemilik: penerimaan di produksi (jawaban < 5 dtk dengan model cepat, primary diblokir tetap dijawab fallback, `/status` setelah sleep 5 menit).

## Fase 7: Worklog
### Fase 7a: Dasar worklog dan kalender Telegram [x] (kode selesai; penerimaan di produksi menunggu deploy)
- [x] Migrasi `0003_worklog.sql` (PRD 5.1; nomor 0003 karena 0002 sudah dipakai status agen), plus kolom `client_id` unik untuk kiriman agen yang idempoten (7d).
- [x] `internal/worklog`: parse/render format berkas bulanan aplikasi lama (`YYYYMM_daily.txt`, header `DD/MM/YYYY`, `* teks`, satu baris kosong antarhari), aturan hari kerja (akhir pekan, libur nasional, cuti bersama tetap hari kerja, override menang), penghitung kelengkapan, pembangun kalender dan keyboard.
- [x] `/log [kemarin|YYYY-MM-DD] teks` (masa depan ditolak, > 2000 karakter ditolak, baris baru jadi spasi) dengan tombol ↩️ Batalkan / ➕ Tambah lagi.
- [x] `/cal [YYYY-MM]`: satu pesan yang diedit di tempat (◀ ▶), tanggal bisa diketuk → isi hari itu dengan ✏️ Edit / 🗑 Hapus (konfirmasi) / ➕ Tambah / ◀ Kembali; edit dan tambah lewat `bot_pending` (berlaku 30 menit, ada ❌ Batal).
- [x] `/libur [YYYY-MM-DD] [catatan]`, `/kerja [YYYY-MM-DD]`, `/libur hapus YYYY-MM-DD`.

Gerbang fase:
- tes tabel aturan hari kerja, parser tanggal (`kemarin`/`YYYY-MM-DD`/`DD/MM/YYYY`/lewat tengah malam), pembangun keyboard, `callback_data` ≤ 64 byte: lulus
- golden test Parse → Render byte-identik (fixture sintetis, termasuk urutan hari tidak kronologis): lulus
- tes integrasi DB (simpan, kalender ✅, lihat hari, edit, tambah, hapus dengan konfirmasi, batalkan, pending kedaluwarsa, override, urutan berkas): lulus
- owner-only pada callback: lulus (`IsOwner` menolak callback akun lain)

Menunggu pemilik: penerimaan di produksi (`/log tes`, `/cal` menampilkan ✅ hari ini, ketuk/edit/hapus, akun lain diabaikan).

### Fase 7b: Libur dan pengingat [x] (kode selesai; penerimaan di produksi menunggu deploy)
- [x] `internal/holidays`: klien `GET /api/holidays?year=` (bentuk respons diverifikasi langsung 2026-11-23; 404 = belum terbit; timeout 10 dtk; maks 3 percobaan) + cache di tabel `holidays`; diperbarui saat start dan tiap hari bila data > 30 hari; mulai 1 November ambil tahun depan; gagal → pakai cache; cache tahun ini kosong → satu peringatan ke pemilik.
- [x] Pengingat slot (6.5):
  - Pemeriksa tiap 30 dtk; slot diklaim tepat sekali (`unique (slot_date, slot)`).
  - Hanya hari kerja; tidak dikirim bila ada catatan dalam 90 menit terakhir, bila sudah "Lewati hari ini", atau sudah mencapai maks per hari.
  - Terlambat > 30 menit (mis. restart) → `expired` tanpa kirim.
  - Tombol ✍️ Tulis sekarang / ⏭ Nanti / 🙈 Lewati hari ini. "Nanti" tidak menambah pesan; penutup jam 17:30 hanya bila slot terakhir ditunda (tanpa "Nanti").
  - Pesan pengingat lama dihapus saat yang baru dikirim; catatan yang tersimpan menutup prompt aktif ("✅ Worklog sudah diisi").
- [x] `/worklog`, `/worklog on|off`, `/worklog jam 10:30,13:30,16:30`, `/worklog penutup 17:30`.
- [x] Event agen `worklog.due` ({prompt_id, slot, count_today}) dan `worklog.closed` ({prompt_id, via}); jawaban dari agen mengedit pesan Telegram.

Gerbang fase (tes dengan jam palsu, semua lulus):
- hari kerja vs akhir pekan;
- libur nasional;
- cuti bersama (tetap diingatkan);
- override `/libur` dan `/kerja`;
- sudah mengisi (dilewati);
- restart di tengah slot (tanpa kirim ganda, kejar ≤ 30 menit);
- "Nanti" tidak menambah pesan;
- penutup hanya setelah slot terakhir ditunda;
- API libur gagal memakai cache;
- maks per hari.

Menunggu pemilik: penerimaan di produksi (tiga pengingat di hari kerja, "Nanti", `/libur`, `/worklog off`).

### Fase 7c: Cadangan GitHub, impor, rekap [x] (kode selesai; penerimaan menunggu pemilik: token + impor)
- [x] `internal/ghbackup`:
  - Setiap perubahan menandai bulannya `dirty` (`changed_at`, migrasi 0004).
  - Setelah 60 dtk tenang, seluruh bulan dirender ke format aplikasi lama dan di-`PUT` dengan `sha` terakhir. Pesan commit `worklog: 2026-11 (N entri)`, tanpa isi catatan.
  - 409/422 → ambil ulang; bila isi remote berubah di luar Tunas → `conflict`, tidak ditimpa, satu peringatan, menunggu `/worklog sync force`.
  - Kegagalan lain: backoff 1, 5, 30 menit, lalu `error` + peringatan (cooldown 1 jam).
  - Token hanya di header; timeout 15 dtk. Nonaktif (satu baris log) bila token/repo kosong.
- [x] Golden test Parse → Render byte-identik dengan fixture sintetis (sejak 7a).
- [x] `tunas-server worklog import [--dry-run] [--repo] [--dir] [--branch]`:
  - idempoten (tanggal + teks);
  - urutan hari tiap berkas dipertahankan;
  - ringkasan hanya berupa angka (tanpa isi);
  - setelah impor, render tiap bulan dibandingkan dengan berkas aslinya, dan `sha` berkas disimpan supaya push berikutnya memperbarui berkas yang sama.
- [x] `/worklog sync [force]`.
- [x] `/rekap [YYYY-MM]` tanpa LLM (hari kerja, terisi, terlewat + tanggalnya, isi per hari); > 4096 karakter → dokumen `rekap-YYYY-MM.md`; tombol 📊 di kalender. `/rekap ... ai` opsional: peringatan sekali dengan tombol konfirmasi, lewat klien LLM yang dihitung di `llm_usage`, teks dibatasi 8000 karakter.

Gerbang fase (server GitHub palsu, semua lulus): sukses + debounce; `sha` basi dengan remote tidak berubah (dicoba ulang); perubahan eksternal = konflik tanpa menimpa; backoff 1/5/30 menit lalu error + satu peringatan; token tidak muncul di log; golden test; impor dry-run/idempoten/render sama.

Menunggu pemilik **[PEMILIK]** (DEPLOY.md bagian 17): buat token fine-grained, isi env, jalankan impor dry-run lalu impor, matikan sinkron aplikasi worklog lama, cek entri baru muncul di repo ≤ 2 menit dan `/rekap 2026-11` cocok.

### Fase 7d: Agen, tray, autostart, bubble worklog [x] (diuji langsung di laptop pemilik terhadap server lokal)
- [x] Menu tray (8.4):
  - Baris status `Tunas · online · sinkron N d lalu` (tidak bisa diklik) dan jumlah catatan worklog yang menunggu sinkron.
  - ☑ Jalankan saat Windows nyala (dibaca dari registri setiap menu dibuka; "Perbarui path" bila exe pindah).
  - ☑ Jeda pelacakan (ikon abu-abu); ☑ Pengingat worklog di layar.
  - Tulis worklog…; Sembunyikan/Tampilkan karakter.
  - Buka log; Buka folder konfigurasi; Keluar.
- [x] Autostart `HKCU\...\Run\Tunas` = path exe dalam kutip + `--background` (tanpa admin; default mati). Tawaran di bubble pada run manual pertama (tidak dengan `--background`).
- [x] `state.json` (tulis atomik) untuk tombol tray; `config.yaml` tidak disentuh. `worklog_bubble_in_focus` di config.
- [x] Bubble worklog:
  - `worklog.due` → form ingat, tanpa merebut fokus; klik/`Alt+Space` → Enter = tulis, N = nanti, Esc = lewati hari ini.
  - Dilewati bila idle ≥ 10 menit, form tidur, form fokus (kecuali diizinkan), atau pengingat sudah > 30 menit (event lama setelah tersambung ulang).
  - `worklog.closed` menutup bubble.
  - `/log teks` di bubble `Alt+Space` langsung menyimpan dengan "Tersimpan √" + loncat kecil.
- [x] Endpoint 6.10: `POST /v1/agent/worklog`, `GET /v1/agent/worklog/today`, `POST /v1/agent/worklog/prompts/{id}/answer`.
- [x] Tahan offline (tambahan pemilik):
  - `client_id` UUID per catatan, disimpan dulu di buffer SQLite (`worklog_queue`).
  - "Tersimpan √ (menunggu sinkron)" bila server tak terjangkau.
  - Server idempoten (`worklog_entries.client_id` unik).
  - Antrean dikirim ulang otomatis (saat tersambung lagi + backoff) dan dihapus hanya setelah dikonfirmasi; jumlahnya tampil di tray.

Gerbang fase:
- build Windows: lulus
- tes state machine bubble worklog, menu tray, `state.json`, penulis registri (antarmuka palsu), aturan tampil bubble: lulus
- tes offline: kirim saat offline → sambung lagi → kirim ulang dua kali → tepat satu baris: lulus
- ukur memori/CPU: working set 28,5 MB (sebelumnya 28,7 MB; tray ≤ +5 MB terpenuhi), CPU tersembunyi 0%

Uji langsung (Windows, server lokal):
- tawaran autostart → Enter menulis registri (`reg query` menampilkan path + `--background`); centang di tray menghapusnya;
- bubble worklog muncul sementara jendela aktif tetap VS Code;
- klik → Enter → tulis → tersimpan sebagai `source=agent` dan prompt `answered/agent`;
- server mati → "menunggu sinkron" + "1 catatan worklog menunggu sinkron" di tray → server hidup → terkirim tepat 1 baris.

Menunggu pemilik **[PEMILIK]**: restart laptop dengan autostart menyala (agen hidup tanpa jendela); menjawab di Telegram menutup bubble; "Jeda pelacakan" menghentikan data masuk.

### Fase 5b (lanjutan 2026-11-25): `/cari` diperbaiki dan gestur "bicara"
- [x] `/cari` (Tavily) sesuai PRD 6.11:
  - Permintaan: `search_depth: basic`, `max_results: 5`, `include_answer: "basic"`, `include_usage: true`, `include_raw_content: false`. `/cari dalam <kueri>` memakai `advanced` (2 kredit). Parameter diverifikasi ke docs.tavily.com.
  - Ke LLM hanya judul + url + 350 karakter pertama untuk maksimal 5 hasil (konteks < 2.500 token); respons mentah tidak pernah diteruskan.
  - Mode jawaban `llm` (default, dirangkum Bahasa Indonesia dengan sumber bernomor) atau `tavily` (jawaban Tavily + 3 sumber, tanpa LLM); `/settings cari_mode llm|tavily`. LLM gagal atau kosong → otomatis jawaban Tavily.
  - Sumber 2 sampai 3 ditulis kode (yang dikutip jawaban, dilengkapi hasil teratas); model tidak pernah menulis URL.
  - Pesan kesalahan spesifik dan tercatat di log (status, durasi, jumlah hasil, kredit; tanpa kunci): 401, 432/433, 429, timeout 8 dtk, kunci kosong, status lain. Total `/cari` ≤ 20 dtk.
  - Kredit dari `usage.credits`: penghitung harian dan bulanan (`search.credits`), peringatan sekali di 80%, jatah habis → tidak memanggil API lagi; baris kredit di `/status`.
  - Dari bubble: `/cari <kueri>` (juga `cari <kueri>`), memakai state mikir lalu bicara_cari.
- [x] Gestur `bicara` / `bicara_cari` (PRD 7):
  - Setelah loncat kecil, jawaban diketik sepanjang `panjang × speech.ms_per_char` (40 ms), dibatasi `speech.min_seconds` (2) dan `speech.max_seconds` (8). Mulut membuka-menutup mengikuti huruf yang muncul, daun bergoyang kecil, badan mengangguk di akhir kalimat, mulut menutup halus lalu kembali ke form sebelumnya.
  - Hasil `/cari`: ditambah ikon kaca pembesar di samping kepala; sumber (nama situs) muncul setelah teks selesai.
  - Berhenti seketika bila bubble ditutup, pemilik menekan Enter / mengetik / `Alt+Space`, atau pengingat jatuh tempo masuk (`ingat` menang). `animations: false` → teks langsung tampil.
  - Config baru: `speech.ms_per_char`, `speech.min_seconds`, `speech.max_seconds`.

Gerbang (semua lulus, termasuk `-race`):
- tes klien Tavily palsu: permintaan PRD 6.11, respons 10 hasil × ±12.000 karakter, 401/429/432/433/timeout/5xx, kunci tidak bocor ke pesan error;
- tes konteks LLM: maks 5 hasil, tiap cuplikan ≤ 350 karakter, total < 2.800 karakter walau respons ribuan karakter;
- tes mode `tavily` (LLM tidak dipanggil), fallback ke `tavily`, sumber 2 sampai 3, angka dari web tidak dibuang, kredit + peringatan 80% + jatah habis tanpa panggilan API;
- tes state machine bicara dengan jam palsu: durasi min/maks mengikuti panjang teks, hop → bicara → kembali, berhenti pada Enter/ketik/Backspace/`Alt+Space`/Esc/klik, `ingat` menang, `bicara_cari` + sumber setelah teks, `animations: false`, anggukan di akhir kalimat.

Belum diuji langsung di layar Windows: gestur bicara dan kaca pembesar (hanya tes unit + gambar pose), dan `/cari` dengan kunci Tavily asli (tidak ada kunci di mesin ini). Memori agen belum diukur ulang untuk perubahan ini.

Menunggu pemilik **[PEMILIK]** (DEPLOY.md bagian 19): isi `TAVILY_API_KEY`, recreate kontainer, uji `/cari`, ganti `tunas-agent.exe`.

## Fase 8: Peningkatan dari pemakaian nyata
### Fase 8a: Worklog multi-baris dan pengingat yang lebih sabar [x] (kode selesai; penerimaan di produksi menunggu deploy)
- [x] `/log` multi-baris (`internal/logbatch`, paket murni yang juga dipakai agen):
  - tiap baris tak kosong = satu catatan; awalan `-`, `*`, `•`, `1.`, `1)` dibuang;
  - baris yang hanya berisi `YYYY-MM-DD`, `kemarin`, atau `hari ini` menetapkan tanggal baris di bawahnya; baris pertama juga boleh `kemarin teks`;
  - batas 50 catatan per pesan dan 2000 karakter per catatan; kelebihan ditolak seluruhnya dengan pesan yang menyebut barisnya, tidak pernah disimpan sebagian; tanggal masa depan ditolak.
- [x] Konfirmasi:
  - satu catatan, atau ≤ 10 tanpa penanda tanggal → langsung tersimpan, `✅ N catatan tersimpan` + `↩️ Batalkan semua` (hanya menghapus catatan dari tempelan itu, lewat kolom `batch_id`);
  - > 10 catatan atau ada penanda tanggal → pratinjau ringkas (jumlah per tanggal) dengan `Simpan` / `Batal` (disimpan di tabel `worklog_batches`, kedaluwarsa 30 menit, sekali pakai).
- [x] Pesan teks setelah `Tulis sekarang` dan `Tambah` memakai aturan yang sama; `Edit` tetap satu catatan (baris baru jadi spasi).
- [x] Idempotensi: server memecah tempelan dari agen dan memberi tiap catatan `client_id` turunan (UUID v5 dari `client_id` paket + nomor baris), jadi kirim ulang dari antrean offline tidak menggandakan.
- [x] Laptop: item tray **Tulis worklog dari clipboard** (pratinjau di bubble, Enter simpan, Esc batal); kotak input bubble dan `/log` menerima `Ctrl+V` multi-baris (maks 8000 karakter); jumlah catatan tampil ("8 catatan tersimpan √").
- [x] Pengingat lebih sabar (PRD 6.5):
  - tombol **Lewati** hanya melewati slot itu, slot berikutnya tetap mengingatkan;
  - pengingat penutup (17:30) muncul hanya bila slot terakhir dilewati, dengan tombol `Tulis sekarang` dan `😴 Senyapkan hari ini`;
  - **Senyapkan hari ini** lewat tombol penutup, `/worklog senyap` (`senyap hapus` untuk membatalkan), item tray "Senyapkan worklog hari ini", atau aksi `mute` dari agen; disimpan di `worklog.muted_date` (hanya berlaku hari itu);
  - bubble: Enter = tulis, N atau Esc = Lewati, S = senyapkan hari ini;
  - tombol "Nanti" dan "Lewati hari ini" lama dihapus; tombol `wln` pada pesan lama dianggap Lewati.

Gerbang fase (semua lulus, termasuk `-race`):
- tes tabel pengurai: berbagai bentuk bullet, penanda tanggal (juga di baris pertama), baris kosong, batas 50 dan 2000 (dalam karakter, bukan byte), tanggal masa depan ditolak;
- tes undo hanya menghapus entri paket itu (entri lama tetap ada, undo kedua → "sudah tidak ada");
- tes idempotensi `client_id` turunan (kirim ulang dua kali → tepat 3 baris, termasuk lewat antrean offline agen);
- tes semantik Lewati/Senyapkan dengan jam palsu (Lewati 10:30 tetap memunculkan 13:30; penutup hanya setelah slot terakhir dilewati; senyap hanya hari itu; jawaban dari agen mengedit pesan Telegram);
- tes pratinjau: > 10 atau bertanggal menunggu `Simpan`, `Batal` tidak menyimpan, pratinjau kedaluwarsa/sekali pakai ditolak; tolak tanpa simpan sebagian.

Belum diuji langsung di Windows: `Ctrl+V`, pembacaan clipboard, dan item tray baru (tes unit untuk logika bubble dan menu; vet Windows bersih). Memori agen tidak diukur ulang.

Menunggu pemilik **[PEMILIK]**: deploy server (migrasi 0005 otomatis) dan ganti `tunas-agent.exe`; uji penerimaan di DEPLOY.md bagian 20.

## Fase 8g: Status dan konsumsi sumber daya (agen, server, VPS)
Cabang `phase-8g-status` (dari `phase-8a-log-batch`).
- [x] Migrasi `0006_stats.sql`: `devices.last_stats`, `stats_samples` (retensi 7 hari, dibersihkan oleh pembersihan harian).
- [x] **Statistik agen** (`internal/agentstats`, `internal/agent/procstats*.go`): sampel tiap 30 detik, dikirim bersama heartbeat (< 1 KB, bentuk tetap tanpa teks bebas); private bytes dan working set lewat `GetProcessMemoryInfo`, CPU lewat `GetProcessTimes`; hanya proses agen sendiri.
- [x] **Statistik server** (`internal/sysstats`): cgroup v2 dan v1, CPU dari selisih `usage_usec`, memori Go, pool pgx, ukuran DB dan lima tabel terbesar.
- [x] **Statistik VPS** tanpa akses Docker/host untuk kontainer: `scripts/host-stats.sh` (cron milik pemilik) menulis `host.json` atomik; server membaca `:ro`, menolak berkas basi (> 5 menit), rusak, terlalu besar (> 64 KB), atau berisi bidang tak dikenal.
- [x] **`/status [agen|vps|llm|24j]`** dengan tombol Segarkan/Agen/VPS/LLM/24 jam yang mengedit pesan di tempat; ✅/⚠️ terhadap target bagian 9; baris LLM per model, kredit pencarian, backup, umur cache libur.
- [x] Cuplikan server dan host tiap 5 menit; puncak dan rata-rata 24 jam di `/status 24j`.
- [x] **Peringatan** (cooldown 1 jam, `/settings ambang` dan `notif_sumber`): memori server > 80%, RAM VPS > 85%, disk > 85%, kontainer tidak sehat, memori agen > 2× target selama 10 menit, LLM harian > 80%.
- [x] **Laptop:** item tray **Status…**, `/status` di bubble (angka lokal langsung + ringkasan server lewat `GET /v1/agent/status/summary`), `tunas-agent.exe --status` (JSON lokal dari `stats.json`, tanpa token).
- [x] Compose: volume `/srv/monitor:/monitor:ro`; `HOST_STATS_FILE` di `.env.example`; versi server lewat build-arg.

Gerbang fase (lulus, termasuk `-race`): pembaca cgroup (fixture v2 dan v1), pengurai `host.json` (basi, rusak, besar, bidang asing), pemformat `/status` (target vs aktual, tanda ✅/⚠️, ambang kustom), ambang peringatan dengan jam palsu dan cooldown (termasuk 10 menit agen), payload heartbeat < 1 KB tanpa teks bebas/judul jendela/token, `host.json` tanpa variabel lingkungan, jalur API heartbeat (bentuk kanonis, payload buruk dibuang, cuplikan 5 menit), hook percobaan LLM.
Biaya pemantauan (benchmark): sampel agen 19 µs / 1,4 KB, snapshot server 30 µs / 0,75 KB.

Belum diuji langsung: angka Private Bytes dan CPU nyata di Windows, item tray Status…, kartu di bubble, cron dan `host.json` di VPS asli.
Menunggu pemilik **[PEMILIK]**: deploy, cron host-stats, `HOST_STATS_FILE` dan volume compose; penerimaan di DEPLOY.md bagian 21.

## Fase 8b: Kategori aplikasi yang bisa diatur dari Telegram
Cabang `phase-8b-apps` (dari `phase-8g-status`).
- [x] Migrasi `0007_app_categories.sql`; pemetaan di server, YAML hanya seed (server mengisi bawaan; agen mengirim YAML sebagai seed tambahan).
- [x] Agen menarik pemetaan lewat `GET /v1/agent/state` dengan etag tiap 60 detik, menyimpan salinan lokal (`categories.json`), dan memakainya untuk mesin form dan bucket.
- [x] `/apps [hari|minggu]` dengan tombol Kerja/Browse/Hiburan/Lainnya, `?` untuk yang belum dikategorikan; `/kategori <app> <kategori>`.
- [x] Kabar sekali per aplikasi baru ≥ 15 menit tanpa kategori; `/apps notif on|off`.
- [x] Seed diperluas (editor, IDE JetBrains, terminal); nama IDE baru seperti Antigravity **tidak ditebak**.

Gerbang fase (lulus, termasuk `-race`): tes pemetaan (huruf besar-kecil, aplikasi baru, berlaku mundur di ringkasan harian), etag state (peta dilewatkan bila etag sama, berubah bila isi berubah, tidak berubah bila hanya waktu ubah), seed tidak menimpa pilihan pemilik dan menolak masukan buruk, kabar sekali (ambang 15 menit, sekali per aplikasi, retry setelah kirim gagal, saklar), agen mengikuti perubahan etag dan salinan lokal.
Belum diuji langsung: tombol di Telegram sungguhan dan nama proses IDE pemilik (harus diverifikasi lewat `/apps`).
Menunggu pemilik **[PEMILIK]**: deploy dan penerimaan di DEPLOY.md bagian 22.

## Fase 8f: Statistik proyek coding dan laporan akhir hari
Cabang `phase-8f-projects` (dari `phase-8b-apps`).
- [x] `internal/projects`: ekstraksi per editor (pola dari config), normalisasi, penolakan nama berkas/jalur/generik, antarmuka pembaca judul (Windows: satu berkas, `GetWindowTextW`; palsu di tes), `--probe-title`.
- [x] Agen: agregasi per menit ke `project_buckets` lokal (hanya editor yang diizinkan, pemilik tidak idle, tidak dijeda, nama terselesaikan, bukan proyek terblok), sinkron idempoten; item tray **Catat proyek editor** (bawaan mati).
- [x] Server: migrasi `0008_projects.sql` (`project_buckets`, `project_aliases`, `project_blocklist`), `POST /v1/agent/projects` dengan validasi ketat, blocklist di `GET /v1/agent/state`.
- [x] `/proyek [hari|minggu|bulan]`, `alias`, `blok`, `buka`, `hapus`; `/today` dan `/rekap` memuat proyek; `/tanya` template `top_projects` (nama proyek tidak ke LLM kecuali `/settings proyek_llm on`).
- [x] Laporan akhir hari (`/laporan on|off|jam|sekarang`) dengan rincian proyek, aplikasi teratas, sesi terpanjang, status worklog, dan saran selisih bertombol **➕ Tambah ke worklog**.
- [ ] 8e (draf worklog per slot) belum; domain teratas di laporan menunggu 8c.

Gerbang fase (lulus, termasuk `-race`): ekstraksi dengan judul sintetis (normal, `●`, extension host, tanpa folder, remote SSH/WSL, nama berkas di depan, tidak cocok pola, jalur, panjang, karakter kontrol, editor kedua); judul asli tidak pernah muncul di log (debug), seluruh isi database lokal, dan payload sinkron; pemindaian repo untuk API judul; alias (gabung lintas editor, berlaku mundur) dan blocklist (server menolak juga); saran selisih dan laporan dengan jam palsu; validasi API (jalur, panjang, huruf, detik, tanggal).
Belum diuji langsung di Windows: pembacaan judul sungguhan, `--probe-title`, item tray, memori/CPU agen dengan pencatatan nyala.
Menunggu pemilik **[PEMILIK]**: setel `window.title`, `--probe-title` untuk editor lain, nyalakan di tray; penerimaan di DEPLOY.md bagian 23.

## Fase 8d: Perintah dan kalender di laptop
Cabang `phase-8d-calendar` (dari `phase-8f-projects`).
- [x] `POST /v1/agent/command` dengan daftar putih (`cal, log, today, tasks, done, status, form, apps, rekap, domain`); perintah lain ditolak dengan "Perintah itu belum ada di laptop, pakai Telegram."; `/status` tetap dijawab di laptop (angka lokal + ringkasan server).
- [x] Endpoint kalender: `GET /v1/agent/worklog/month?m=`, `GET .../day?d=`, `PATCH` dan `DELETE .../entries/{id}`; aturan hari dihitung di server.
- [x] Kartu kalender (item tray **Kalender worklog…** dan `/cal`): grid 7 kolom Senin-Minggu, penanda titik/cincin tanpa emoji, "N/M hari kerja terisi", nama hari libur saat dipilih; panel hari dengan daftar, kotak ketik (`Enter` tambah, `Ctrl+V` multi-baris), `E` ubah, `Del` hapus dengan konfirmasi.
- [x] Papan ketik: panah, `PgUp`/`PgDn`, `T`, `Enter`, `Esc`; kartu tidak membuka sendiri.
- [x] Offline: cache bulan/hari ("data lama"), tulisan lewat antrean worklog.

Gerbang fase (lulus): tata letak (28/30/31 hari, mulai Senin, 4/5/6 baris, semua sel di dalam kartu dan di atas footer); navigasi lintas bulan dan tahun; penanda dan ringkasan sama dengan Telegram; panel hari (tambah, ubah, hapus dengan konfirmasi, tempel multi-baris, batas panjang); daftar putih perintah (tidak dikenal ditolak tanpa menyentuh pengendali); cache offline (bulan/hari, 5xx vs 4xx, batas, file rusak, restart); penggambaran ke kanvas memori; semua teks punya glyph; build Windows.
Belum diukur: memori kartu terbuka dan tertutup di Windows asli, tampilan di layar.
Menunggu pemilik **[PEMILIK]**: deploy dan penerimaan di DEPLOY.md bagian 24.

## Fase 8c: Domain situs dan teguran saat kelamaan (opt-in)
Cabang `phase-8c-domains` (dari `phase-8d-calendar`).
- [x] `internal/domains`: normalisasi hostname (tes tabel + fuzz + tes JS yang sama), cocok per akhiran.
- [x] Jembatan lokal `internal/bridge` (loopback, Host, Origin persis, token, badan ≤ 1 KB, CORS tepat) yang hanya jalan saat "Catat domain situs" menyala.
- [x] Ekstensi MV3 di `extension/` (izin `tabs` dan `storage`, tanpa penyamaran, kirim `{host, ts}`), ID tetap, halaman opsi dengan tes koneksi.
- [x] Agen: agregasi per menit ke `domain_buckets` lokal, hanya browser dikenal dan pemilik tidak idle/dijeda, sinyal basi dibuang, sinkron idempoten; item tray **Catat domain situs** dan **Salin token ekstensi browser**.
- [x] Server: migrasi `0009_domains.sql`, `POST /v1/agent/domains` (hostname saja), kategori awal, `/domain` (daftar, kategori, blok, buka, hapus), situs baru ≥ 15 menit ditanyakan sekali, `/today` memuat 5 situs, `/tanya top_domains`, ringkasan harian ikut (ke LLM hanya bila `domain_llm`).
- [x] Teguran: `forms.Nudge` (ambang, jeda, snooze, cooldown, batas harian), bubble dua pilihan, `/tegur on|off`, alias konfigurasi lama.

Gerbang fase (lulus): validasi hostname dan fuzz (path, query, fragmen, kredensial, IDN, IP, localhost); keamanan jembatan (non-loopback, Origin palsu atau kosong, token salah, badan besar, Host salah, preflight tepat); agregasi dengan jam palsu (sinyal basi, fokus browser, blocklist termasuk subdomain, idle, jeda); teguran (ambang, jeda < 2 menit, snooze, cooldown, batas harian, mati, tidur, aplikasi dan situs, jeda pelacakan); tes manifest (izin, ID, tanpa penyamaran) dan sumber ekstensi; semua teks bubble punya glyph.
Belum diuji langsung: ekstensi di browser, bubble teguran di layar, memori agen dengan jembatan nyala.
Menunggu pemilik **[PEMILIK]**: pasang ekstensi dan nyalakan di tray; penerimaan di DEPLOY.md bagian 25.
