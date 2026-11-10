# Read-only diagnostic fork

This is **fmaranhao/mcp-raker**, an owner-requested diagnostic fork of [lexfrei/mcp-raker](https://github.com/lexfrei/mcp-raker), based on commit `1325f0a2323013ef35a4c1f261213a06c9b60991`. See [design](docs/readonly-design.md), [plan](docs/readonly-plan.md) and [two-container deployment](deploy/README.md). Implementation refers to [IT Projects issue #81](https://github.com/fmaranhao/IT-Projects-Handbook/issues/81).

**This fork's executable is always read-only.** It registers 22 explicit diagnostic tools and enforces an independent backend GET/route/query/origin allowlist. Operational tools cannot be enabled; `MOONRAKER_ENABLE_ADMIN=true` fails startup. Config, G-code and log reads under Moonraker's registered printer_data roots are allowed by the owner, with bounded file ranges/tails. No raw proxy or arbitrary host-file access. Data reads can disclose secrets; no automatic redaction is claimed.

The original upstream README below describes capabilities retained in source for comparison and upstream tests, **not tools available from this fork's executable**. Its operational examples/admin instructions do not apply. Use the deployment runbook above. No live printer connection or published fork image is claimed.

---

# mcp-raker

[![CI](https://github.com/lexfrei/mcp-raker/actions/workflows/ci.yml/badge.svg)](https://github.com/lexfrei/mcp-raker/actions/workflows/ci.yml)
[![Release](https://github.com/lexfrei/mcp-raker/actions/workflows/release.yml/badge.svg)](https://github.com/lexfrei/mcp-raker/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/lexfrei/mcp-raker)](https://goreportcard.com/report/github.com/lexfrei/mcp-raker)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD%203--Clause-blue.svg)](LICENSE)

An MCP server for [Moonraker](https://moonraker.readthedocs.io/), the API server that fronts the Klipper 3D-printer firmware. It exposes the Moonraker HTTP API as MCP tools so an assistant can monitor and control a printer.

## Highlights

- Broad coverage of the Moonraker REST API: status, printing, G-code, files, history, the job queue, machine and OS control, the update manager, power devices, WLED, sensors, webcams, Spoolman, the database, access control, announcements, analysis, MQTT, and extensions.
- Works against an unauthenticated printer on a trusted LAN out of the box; supports an API key or a username/password JWT login when the printer requires it.
- Destructive OS, service, update, and user-management tools are gated behind a flag and disabled by default.
- Tools carry read-only, write, and destructive annotations so clients can warn before acting.
- Predictable output: every tool returns its data as a top-level JSON object, with no per-tool envelope to unwrap.
- Ships as a small, signed, multi-arch container image.

## Tools

Tools are named `moonraker_*`. The everyday set is always available; the destructive admin set is registered only when `MOONRAKER_ENABLE_ADMIN=true`.

Always available:

- **Status**: server info and config, printer info, object list and query, endstops, temperature and G-code stores.
- **Printing**: start, pause, resume, and cancel prints; run G-code; emergency stop; restart Klipper and the firmware.
- **Files**: list, browse, metadata, thumbnails, create/delete directories, move, copy, zip, download, upload, delete.
- **History and queue**: list and inspect jobs, totals, and the job queue (enqueue, remove, pause, start, jump).
- **Machine**: system info, process stats, peripherals; power devices; sensors; WLED.
- **Integrations**: Spoolman, the database, announcements, webcams, analysis, MQTT, extensions, notifiers, update status.

`moonraker_spoolman_proxy` forwards an arbitrary method, path, and body to the configured Spoolman server, so it can perform writes and deletes there; it is marked destructive and is scoped to the Spoolman host Moonraker is configured with.

Admin (require `MOONRAKER_ENABLE_ADMIN=true`):

- OS shutdown and reboot, systemd service control, sudo password.
- The update manager (refresh, upgrade, recover, rollback).
- Moonraker server restart and log rollover. (Klipper and firmware restart are not gated — they are printer actions, listed under Printing above.)
- User management and API-key regeneration.

## Output

Every tool returns its result as a top-level JSON object — read the fields directly, never through a `result` wrapper. Endpoints that return a bare list upstream wrap it under a descriptive key (`files`, `roots`, `thumbnails`); action tools that only succeed return `{"ok": true}`. `moonraker_spoolman_proxy` surfaces Moonraker's v2 proxy envelope (its `response` and `error` fields) at the top level. `moonraker_extensions_request` and `moonraker_mqtt_subscribe`, whose payloads can be any JSON value, wrap it under a single key (`response` and `payload` respectively) so the result stays a JSON object even for a bare scalar or array.

Two reads trim their payload by default to keep an assistant's context small:

- `moonraker_proc_stats` returns the last 5 CPU-history points; pass `samples` for more, or `0` (or less) for the full history.
- `moonraker_history_list` drops each job's gcode thumbnails; pass `include_thumbnails: true` to keep them.

`moonraker_sensors_list` and `moonraker_sensors_measurements` return an empty result instead of an error when the printer has no `[sensor]` section configured.

The `moonraker_mcp_version` tool reports the running build's version and git revision: stamped from build flags in the container image, and otherwise from the embedded Go build info (so `go install` and local builds self-identify too).

## Authentication

Moonraker on a trusted LAN often needs no authentication, and that is the default. When the printer enforces authentication, provide one of:

- `MOONRAKER_API_KEY` — sent as the `X-Api-Key` header on every request. Simplest for a headless client.
- `MOONRAKER_TOKEN` — a pre-obtained JWT, sent as a Bearer token.
- `MOONRAKER_USERNAME` and `MOONRAKER_PASSWORD` — logs in via `/access/login`, caches the access and refresh tokens, and refreshes automatically when the token expires.

## Configuration

All configuration is read from environment variables.

| Variable | Purpose | Default |
| --- | --- | --- |
| `MOONRAKER_URL` | Moonraker base URL | `http://localhost:7125` |
| `MOONRAKER_API_KEY` | `X-Api-Key` header value | — |
| `MOONRAKER_TOKEN` | Pre-obtained Bearer (JWT) token | — |
| `MOONRAKER_USERNAME` / `MOONRAKER_PASSWORD` | Credentials for JWT login | — |
| `MOONRAKER_TOKEN_FILE` | Path to cache the session token | `~/.mcp-raker/token.json` |
| `MOONRAKER_ENABLE_ADMIN` | Enable destructive admin tools | `false` |
| `MOONRAKER_USER_AGENT` | HTTP User-Agent | `mcp-raker` |
| `MOONRAKER_PROXY` | HTTP/SOCKS5 proxy URL | — |
| `MOONRAKER_TIMEOUT` | Per-request timeout (Go duration) | `30s` |
| `MCP_HTTP_PORT` | Enable the HTTP transport on this port | — |
| `MCP_HTTP_HOST` | HTTP bind host | `127.0.0.1` |
| `MCP_HTTP_TOKEN` | Bearer token required on HTTP requests | — |

## HTTP transport security

The server speaks MCP over stdio by default. Setting `MCP_HTTP_PORT` also starts an HTTP transport. The HTTP transport has no per-request authentication of its own, so the server refuses to bind it to a non-loopback host unless `MCP_HTTP_TOKEN` is set; with a token, every request must carry `Authorization: Bearer <token>`.

## Usage

Run the published container image and point it at your printer. Example MCP client configuration (`.mcp.json`):

```json
{
  "mcpServers": {
    "mcp-raker": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "-e", "MOONRAKER_URL",
        "-e", "MOONRAKER_API_KEY",
        "-v", "mcp-raker-session:/home/nobody/.mcp-raker",
        "ghcr.io/lexfrei/mcp-raker:latest"
      ],
      "env": {
        "MOONRAKER_URL": "http://printer.local:7125"
      }
    }
  }
}
```

Set `MOONRAKER_ENABLE_ADMIN` to `true` in `env` only if you want the destructive OS, service, update, and user-management tools.

## Development

```bash
go build ./cmd/mcp-raker
go test -race ./...
golangci-lint run
```

## License

BSD 3-Clause. See [LICENSE](LICENSE).
