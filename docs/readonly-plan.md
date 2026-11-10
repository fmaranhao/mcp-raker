# Read-only fork implementation plan

> Execute inline under the owner's instruction to proceed. Spec: readonly-design.md.

**Goal:** preserve upstream container/HTTP MCP functionality while enforcing zero printer operations and permitting bounded printer_data/log reads.

**Files and steps**

1. `internal/moonraker/readonly.go`, client/request integration and tests: first add failing allowlist/redirect/bounds tests; implement exact GET/path/query/origin policy before network, read-only production client and ranged raw reads. Full upstream clients remain testable with explicit read-only option; executable always selects it.
2. `cmd/mcp-raker/main.go`, `readonly.go` and tests: first add catalogue/call rejection tests; replace operational registration, reject admin/login settings and expose bounded file-tail reads. Retain HTTP bearer behavior. Run full suite/race/build.
3. `deploy/compose.yaml`, protected env example and fork README: DNS-only two-container source-build recipe, no host ports, root/socket mounts or runtime credential material. Document file disclosure limits and owner commissioning/rollback.
4. Fresh safety review, fix blockers with regressions, repeat checks, push task branch and open fork PR. Open handbook documentation PR and update #81 with authorized file access and exact validation limits. No live printer or release/image publication.

**Global constraints:** hard read-only executable, server-side method/path/query allowlist, no operational opt-out, bounded file reads, config/gcodes/logs roots only, no secrets in Git, DNS target, Fred merges/deploys.

**Review focus:** redirect bypass; double-encoded traversal/duplicate query values; GET endpoints with side effects; large/ignored-range log responses; new upstream tools accidentally registering. Tests attach to the boundary that owns each.

## Execution ledger

- Fork fmaranhao/mcp-raker created from upstream master; local isolated clone and branch codex-voron-readonly-moonraker. Upstream base 1325f0a2323013ef35a4c1f261213a06c9b60991. Existing project directories untouched.
- Terminal GitHub access works in this turn. Local Docker socket is unavailable. Downloaded exact upstream Go 1.27.3 toolchain to task scratch, verified against official go.dev checksum; no global installation.
- Tasks 1–3 complete. Upstream baseline and final full suite pass. Final `go test -race -count=1 ./...`, `go vet ./...`, golangci-lint v2.15.0 (0 issues), Linux amd64 CGO-disabled cross-build, Compose config --quiet with dummy credentials and whitespace checks passed.
- Independent review found two blockers: invalid-config credential echo and Docker copying deploy/.env into build context. Both were reproduced/addressed; re-review independently confirmed fixes and passing tests. Also validated Content-Range, exact 22-tool catalogue and original byte counts for text conversion. HTTP now serves MCP only at /mcp; unknown/OAuth metadata routes correctly return 404, covered by a failing-then-passing regression.
- Task 4: fork/handbook PR handoff. No container runtime build/run (local Docker socket denied), production API/tunnel connection, published image/release or live printer action was performed. Happy Hare data completeness remains a live check; #81 stays open.
