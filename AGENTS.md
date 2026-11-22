# AGENTS.md

Notes for AI coding agents working in this repo.

- Spec: `docs/PRD.md` (local only, not in git). Status per phase: `PROGRESS.md`.
- **Never commit anything under `docs/`** (it is git-ignored on purpose).
- Code, identifiers, comments, commit messages: English. User-facing text (bot replies, character speech): Bahasa Indonesia.
- Never print, log, or commit secrets (bot token, device tokens, DB password, API keys).
- Before pushing: `gofmt -l .` empty, `go vet ./...`, `go test ./...`, `GOOS=windows go build ./cmd/agent`.
- Windows-only code uses `//go:build windows` behind interfaces so logic is testable on Linux.
- Keep it simple: stdlib `net/http`, no frameworks, no speculative abstractions.
