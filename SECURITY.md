# Security Policy

## Security considerations

The server runs on the machine of the person who starts it. It logs in to their mail and calendar servers and acts for an agent. The agent counts as an untrusted caller, and mail and invites count as hostile input.

- The person sets every trust-bearing option: the accounts, the servers, `--read-only` and `--tools`. No tool argument can change them.
- The password stays in the OS keychain, or in `PIM_MCP_PASSWORD` for a single account. It never appears in a tool result, a log or the config file.
- Every connection is encrypted and its certificate verified. Plain connections are allowed to this machine only, for tests.
- No tool sends, moves or deletes mail. Reading leaves mail unread.
- Every calendar write names the version it read. A write to an event that changed since fails.
- An invite or message cannot change the server's options. Its text is escaped before it is stored, and its size and recurrence are limited.

Prompt injection stays possible: a message can try to talk the agent into a calendar change. Use `--read-only`, `--tools`, and a client that asks before a write. The README's [Security](README.md#security) section has the details and the limits.

## Supported versions

Fixes go into the latest release only.

## Reporting a vulnerability

Do not open a public GitHub issue for a security vulnerability.

Use [GitHub's private vulnerability form](https://github.com/robinvdvleuten/pim-mcp/security/advisories/new).

Include the affected code, its impact, and steps that reproduce the issue. Tell us whether and how to credit you.

We do not accept AI slop reports.

Keep the report private while we investigate and prepare a fix.
