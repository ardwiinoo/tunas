# Progress

## Fase 0: Fondasi repo dan CI/CD [~]
- [x] `go.mod`, tata letak repo, `Makefile` (`test`, `lint`, `build`, `build-agent`)
- [x] `cmd/server`: `serve` dengan `/healthz`, `healthcheck`, muat konfigurasi, log slog JSON
- [x] `cmd/agent`: stub yang membaca `config.yaml`
- [x] Dockerfile, `docker-compose.yml`, `.env.example`, `caddy-site.conf`
- [x] Workflow `ci.yml`, `deploy.yml`, `release-agent.yml`
- [x] `README.md`, `AGENTS.md`, `PROGRESS.md` (+ DEPLOY/DECISIONS lokal)
- [x] `.gitignore`

Gerbang fase:
- gofmt/vet/test: -
- docker build lokal: -
- CI GitHub: -
- build Windows: -

Catatan / keputusan:
- Go 1.27 (1.25 sudah tidak dirawat).
- Deploy otomatis melewati langkah SSH sampai secret `VPS_HOST` diisi.

Angka ukur (memori, CPU): -

Menunggu pemilik:
- Langkah deploy 2 sampai 6 (secrets, DB, `/srv/tunas`, DNS, Caddy). Penerimaan `curl https://tunas.ardwiinoo.my.id/healthz` menunggu ini.
