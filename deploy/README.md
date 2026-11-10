# Owner deployment — read-only Moonraker MCP

Prepared for Docker-AMD; not deployed or connected to a live printer by this task. Refs [handbook #81](https://github.com/fmaranhao/IT-Projects-Handbook/issues/81). Fred merges the fork PR into its upstream-derived `master` and the handbook PR into `main`. The fork keeps upstream's module path for a small mergeable patch; fork provenance is recorded in the root README and design.

## Scope and data access

The executable always selects the read-only backend and fixed 22-tool catalogue. No flag restores printer operations; `MOONRAKER_ENABLE_ADMIN=true` is rejected. Keeping upstream operational handler source/tests does not register those tools or permit their requests in the production client. Both HTTP and stdio transports use the same restricted server/client.

Includes status, temperatures, cached console/errors, printer objects, history/queue reads, system/sensor observations and config/gcodes/logs file reads. Happy Hare/Kalico data is queried through whatever objects the live instance exposes; no dedicated MMU control macros are run. Logs default to a last-64-KiB read; file slices are capped at 256 KiB and other responses at 4 MiB. Oversized or ignored Range responses fail rather than being downloaded unboundedly.

`printer_data` access means Moonraker's registered `config`, `gcodes` and `logs` roots. It does not expose the entire directory, its database, arbitrary host paths or new roots. There is no host filesystem mount or SSH credential. Moonraker still enforces its protected-file and symlink rules. Files/objects/logs may contain credentials; read-only is not redaction. Fred explicitly permits these reads, but never commit or indiscriminately paste raw contents.

## Two-container layout

- `moonraker-mcp`: versioned read-only fork image; fixed DNS upstream `http://voron-klipper.maranhao.cc:7125`. Moonraker stays on the printer. No changes to Kalico, Happy Hare, Mainsail or printer firmware.
- `openai-tunnel-moonraker`: existing OpenAI tunnel pattern, separate tunnel ID and dedicated MCP bearer token. HTTP discovery and normal requests receive the token through protected environment references.
- Both on an ordinary private Docker bridge, with outbound connectivity and no host-published ports. No Docker socket, devices or privileged mode. Moonraker MCP is non-root via the upstream scratch image and runs with a read-only filesystem. The tunnel has only a read-only profile and bounded temporary storage.

Moonraker's existing documented trusted-client ranges may or may not include this bridge's translated source. Do not broaden trusted_clients automatically. If authentication is required, use a locally held Moonraker API key; the backend policy, not that broad key, restricts operations. Username/password JWT login is rejected to avoid authorizing extra POST routes. Static token is supported by the executable if separately configured, but the supplied Compose uses API key/trusted LAN only. HTTPS upstream URLs use Go's normal certificate validation; this recipe uses the existing private LAN HTTP listener.

## Pull and inspect before connecting a tunnel

Use the reviewed deploy files from `fmaranhao/mcp-raker`. The Compose recipe pulls `ghcr.io/fmaranhao/mcp-raker:0.3.0`, built by the fork's Release workflow from tag `v0.3.0` (merged commit `b6fe4d8343c40852b194643bf20cfb4626099e04`). The multi-platform image supports linux/amd64 and linux/arm64. No source checkout/build context is needed on the deployment host. Publication is separate from live commissioning.

Verified multi-platform manifest digest: `sha256:8287607b0a820fd234ccbc66ed90dba8fa906295146739769bbbfd8c48c31967`. To pin it, use `ghcr.io/fmaranhao/mcp-raker:0.3.0@sha256:8287607b0a820fd234ccbc66ed90dba8fa906295146739769bbbfd8c48c31967`. [Release build](https://github.com/fmaranhao/mcp-raker/actions/runs/42732442576) built both platforms, merged the manifest and signed the tags.

```bash
cd /path/to/mcp-raker/deploy
cp .env.example .env
chmod 600 .env
```

Fill `.env` locally: generate a dedicated random MCP_AUTH_TOKEN, use the existing protected runtime key where appropriate, and create a separate Moonraker tunnel using the same owner-managed process as Arcane/MikroTik/Technitium. Do not paste keys into chat or Git.

Validate Compose without printing its interpolated credentials, then pull/start **only** the MCP first:

```bash
docker compose config --quiet
docker compose pull moonraker-mcp
docker compose up -d moonraker-mcp
docker compose logs --tail=30 moonraker-mcp
```

MCP HTTP bearer authentication is required when binding to the bridge. No host port needs opening. A running process alone does not prove catalogue or upstream readiness. The tunnel configuration waits up to 90 seconds for startup; actual container/tunnel compatibility remains to be checked on Docker-AMD.

The HTTP handler uses sessionless mode so SDK discovery can advertise and serve MCP 2026-07-28; session-based mode supports only earlier protocol versions. Legacy 2025-11-25 clients remain covered by read/authentication tests. Diagnostic tools require no persistent client session or standalone SSE subscription. This transport choice does not change the tool catalogue or upstream request policy.

Then start the tunnel and check readiness/logs before connecting the ChatGPT developer plugin:

```bash
docker compose up -d openai-tunnel-moonraker
docker compose logs --tail=40 openai-tunnel-moonraker
```

Use a separate plugin/tunnel registration for the printer. No OpenAI admin key belongs in either container. This task does not create that registration or deploy the pair. Container logs may include error/path metadata; inspect locally before sharing.

## Commission without writes

While the printer is safely idle, perform MCP discovery and these representative reads through the tunnel:

1. `moonraker_mcp_version`, `moonraker_server_info`, `moonraker_printer_info`.
2. `moonraker_objects_list`, then `moonraker_objects_query` for temperatures/print state and any actual MMU object. Record missing Happy Hare fields rather than guessing compatibility.
3. `moonraker_history_list`, then `moonraker_gcode_store` for an existing historical error.
4. `moonraker_files_roots`, config-directory listing and a small known config file; then `moonraker_files_download` with root logs/filename klippy.log to verify bounded log tail. Moonraker file permissions or symlink protection may reject some reads; do not bypass them.
5. Catalogue must contain only the 22 reviewed tools. There must be no G-code script, heater, movement, print control, file write/delete, power, emergency-stop, update or generic API/proxy tool. Local automated tests already attempt prohibited calls against a mock Moonraker; do not test operational commands against the live printer.

Confirm actual plugin reads, unauthorized HTTP rejection and container isolation before calling deployed/commissioned. Leave #81 open until recorded. No printer start/pause/heater/homing action is needed for testing.

## Maintenance and rollback

Use explicit reviewed fork release versions (or their immutable manifest digests). Review upstream changes and test each release before updating only this pair. Do not blindly auto-merge upstream or track its operational image under this service name. Retain the prior image version/digest and protected .env/profile outside Git. Existing general container update policies should not replace this fork image with an unreviewed upstream build. Avoid tracking latest during commissioning.

For withdrawal or a failed update, stop the new tunnel first, restore the prior reviewed image/source if appropriate, restart only this pair and revalidate reads. Otherwise keep the pair stopped and revoke its separate tunnel/plugin registration and credentials as needed. Existing Mainsail/Moonraker are independent and stay in place. Do not use `docker compose down` against the whole existing ai-mcp project.

When adding to the existing Arcane ai-mcp project, merge these two service definitions, this bridge and the read-only profile mount into its saved Compose using absolute host paths. Do not replace that project's entire Compose file. Use the versioned GHCR image and omit build from the saved project; this requires no third container or printer_data mount.
