# F3 Denver Help Desk

A web-based ticketing system for F3 Denver PAX to submit help requests for preblasts, backblasts, achievement tracking (Yeti), the F3 Denver website, or other issues.

## Features

- **Ticket types**: Preblast, Backblast, Yeti (achievement tracking), Website, Other
- **SQLite backend** with data persisted on a Podman volume
- **Admin dashboard** (password-protected) to view and manage tickets
- **Editable tips/troubleshooting page** (markdown) for self-service help
- **F3 Denver themed** UI

## Quick Start with Podman

```bash
# Build
podman build -t f3denver-help .

# Run with persistent data volume
podman volume create f3help-data
podman run -p 8080:8080 \
  -v f3help-data:/data \
  -e ADMIN_PASSWORD=your-secret \
  f3denver-help
```

Open http://localhost:8080.

## Quick Start (local dev)

Requires Go 1.23+ with CGO enabled (for SQLite).

```bash
cp .env.example .env
# Edit .env with ADMIN_PASSWORD
go run .
```

## Environment Variables

| Variable       | Required | Default | Description                    |
| -------------- | -------- | ------- | ------------------------------ |
| ADMIN_PASSWORD | Yes      | —       | Password for admin panel       |
| SERVER_PORT    | No       | 8080    | HTTP listen port               |
| DATA_DIR       | No       | ./data  | SQLite DB and tips.md location |

## Routes

- `/` — Submit a ticket
- `/tickets` — Look up your tickets by F3 name
- `/tips` — Tips and troubleshooting (markdown)
- `/admin` — Admin dashboard (requires login)
- `/admin/tips` — Edit tips markdown
- `/healthz` — Health check

## Deployment (Linode)

See `linode/Caddyfile` and `linode/f3denver-help.service`. Build with `./build.sh` and push to `quay.io/migtools/f3denver-help`.
