# Read-only Moonraker fork — 2026-11-10

Refs fmaranhao/IT-Projects-Handbook#81. Owner approved a read-only fork of lexfrei/mcp-raker and explicitly permits printer_data and log reads. No live printer operations are authorized by this development task.

## Approach

Keep upstream Go MCP transport, authentication, container and typed diagnostic handlers. Replace executable registration with a fixed diagnostic tool list and always enable a second backend HTTP allowlist. Stock upstream exposes operations even with admin disabled; a custom replacement would duplicate its useful transport/tool work. No configuration flag may restore operational tools in this fork's executable.

## Scope

Read printer/server state, temperatures, cached console errors, object state (including whatever Kalico/Happy Hare actually exposes), job history/queue state, system/sensor measurements and Moonraker file roots. File content access covers the config, gcodes and logs roots within printer_data as exposed by Moonraker, not arbitrary host paths or all directories beneath printer_data. No extra printer-host service or filesystem mount is needed. Additional unexposed printer_data directories are not silently made accessible.

File reads can reveal secrets; Fred permits these diagnostic reads. Keep credentials and raw configuration/log contents out of Git and routine summaries. Do not claim automatic secret redaction. Provide bounded range/tail reads for large logs. Rely on Moonraker's protected-file and symlink handling within registered roots; do not claim remote filesystem realpath enforcement by this container.

## Enforcement

- Explicit tool registrations: no inferred safe-name prefixes, generic proxy, access-key retrieval or operational handlers.
- Backend: GET-only exact route allowlist with endpoint-specific query keys, rooted file paths and rejected traversal/encoding tricks. No G-code, mutations, file writes, websocket/JSON-RPC forwarding, MQTT, power, update actions or emergency stop. New upstream endpoints are denied by default.
- Enforce before dispatch and on the HTTP transport; reject redirects, alternate origins and userinfo/query/fragment/base-path URL tricks. Restrict production upstream authentication to API key/static token/trusted LAN; username/password login is rejected because it would require extra POST endpoints.
- Bound responses and range reads; preserve upstream TLS certificate verification. No credentials in error messages or startup URL logs.

## Deployment and acceptance

Two services in Docker-AMD's ai-mcp project: fork image plus existing OpenAI tunnel, private bridge, no host ports/socket, DNS upstream voron-klipper.maranhao.cc:7125. MCP HTTP requires a bearer token; tunnel carries it. Read-only root filesystem, non-root executable, tmpfs only where needed. Build fork on a task branch and open PR into upstream-derived master; handbook docs use a separate task branch from main. Fred merges. Do not publish a release or deploy to production in this task; provide source-build deployment to avoid claiming an unbuilt image exists.

Tests must verify exact catalogue, direct forbidden MCP calls, all write verbs and GET action routes, redirects/cross-origin/auth, malicious encoded paths/queries, file range limits and valid config/log/MMU reads against a local mock server. Full upstream suite, race detector and Linux build run where supported. Container/runtime tests and live printer/plugin commissioning are separate evidence gates; #81 remains open.
