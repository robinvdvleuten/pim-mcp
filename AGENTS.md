# pim-mcp

- **Check a change:** `mise run check` (format, unit tests, lint, vulnerability scan). It uses the pinned toolchain; a bare `go` or `golangci-lint` may be the wrong version.
- **Integration tests:** `mise run integration` starts fresh Docker servers, runs the suite and removes them. Set `PIM_MCP_TEST_STACK=1` (or any other number) to run beside another stack. `PIM_MCP_TEST_NEXTCLOUD=1` adds Nextcloud.
- **Why the code is the way it is:** [DESIGN.md](DESIGN.md). Read the record for the area before changing it.
- **Reviewing:** [CODING_STANDARDS.md](CODING_STANDARDS.md).
- **Commits:** Conventional Commits without a scope. The `commit-msg` hook lints the message with Vale.
- `SPEC.md`, `tasks/` and `docs/` are the owner's local notes and are not tracked. Leave them as they are.
