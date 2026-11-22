# Tunas

Asisten pribadi 24/7 untuk satu pengguna: bot Telegram (tugas, pengingat, ringkasan aktivitas) di VPS, dan agen Windows yang mencatat aktivitas laptop serta menampilkan karakter "Tunas" di layar.

## Bagian

| Bagian | Lokasi | Keterangan |
|---|---|---|
| Server | `cmd/server` | bot Telegram (long polling), scheduler pengingat, API agen. Docker, di-deploy otomatis dari `main`. |
| Agen | `cmd/agent` | `tunas-agent.exe` untuk Windows: pelacak aplikasi aktif, sinkron, karakter Tunas di layar (Alt+Space untuk ngobrol). |

## Perintah Telegram

`/task`, `/tasks`, `/done`, `/remind`, `/reminders`, `/cancel`, `/today`, `/tanya`, `/form`, `/cari`, `/export`, `/ping`, `/help`. Kalimat biasa ("ingatkan aku jam 5 sore kirim laporan") dipahami lewat LLM dan selalu dikonfirmasi dengan tombol Ya/Batal.

## Skrip

- `scripts/backup-db.sh`, `scripts/restore-db.sh`: backup harian `pg_dump` (simpan 7) dan restore ke database baru.
- `scripts/measure-agent.ps1`: ukur memori/CPU agen di Windows.

## Pengembangan

Butuh Go (versi di `go.mod`; `GOTOOLCHAIN=auto` mengunduhnya otomatis).

```sh
make lint         # gofmt + go vet
make test         # tes unit (+ integrasi jika TEST_DATABASE_URL diisi)
make build        # bin/tunas-server
make build-agent  # bin/tunas-agent.exe (Windows)
```

Tes integrasi Postgres berjalan bila `TEST_DATABASE_URL` diisi, misalnya:

```sh
docker run -d --name tunas-pg -e POSTGRES_PASSWORD=tunas -p 5432:5432 postgres:17-alpine
export TEST_DATABASE_URL=postgresql://postgres:tunas@localhost:5432/postgres?sslmode=disable
```

Konfigurasi server lewat environment variable (lihat `.env.example`). Pakai token bot terpisah untuk pengembangan lokal: hanya satu proses boleh long polling per token.

## Subcommand server

```
tunas-server [serve]                     jalankan server
tunas-server healthcheck                 exit 0 bila /healthz lokal OK
tunas-server device create --name NAMA   buat token perangkat (tampil sekali)
tunas-server device list
tunas-server device revoke --name NAMA
```

## Privasi

Agen hanya mengirim nama proses, kategori, dan durasi per menit. Tidak ada judul jendela, URL, isi layar, atau ketikan.
