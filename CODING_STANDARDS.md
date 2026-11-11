# Coding standards

For review. These are the judgement calls no linter makes. `mise run check` covers formatting, lint, tests and known vulnerabilities, so a reviewer need not.

[DESIGN.md](DESIGN.md) holds the decisions behind the design. A change that contradicts a record there either updates the record, with its reason, or is wrong.

## Hostile input

Mail, invites and server responses are written by strangers.

- **Linear.** Code that reads them does work proportional to their size, and a test feeds it the worst case: deep nesting, unmatched tags, a rule that never ends.
- **Bounded.** Every loop over their content has a limit, and hitting it is an error that names the limit. A result is cut only where cutting is the point, and then a field says so.
- **Fuzzed.** A third-party parser called on them has a fuzz target, and a panic inside it is caught and returned as an error.
- **Escaped.** Their text reaches a calendar object only through the library's escaping.

## Trust boundaries

- **IDs.** A path from the agent is checked against the calendar home inside `internal/calendar`, before any request for it.
- **Writes.** Every PUT and DELETE carries `If-Match` or `If-None-Match`. No code path writes without one.
- **Credentials.** A password goes only to the servers of its own account, over TLS, and never into the config file, a flag, a log or an error message.
- **Mail.** Nothing changes a mailbox. Folders are opened read-only and parts fetched with PEEK.
- **Logs.** stderr only. Tool arguments and results are never logged, not even an email address.

## Shape

- **Thin handlers.** A tool handler validates its input, calls a domain package and maps the result. Logic lives in `internal/ical`, `internal/calendar`, `internal/mail` or `internal/importer`.
- **One place.** A rule that must hold everywhere is enforced in one function that everything passes through, as `addTool` does for read-only mode. A check repeated per caller will be missed by the next caller.
- **Read-only by default.** A new tool that changes nothing says so with `ReadOnlyHint`. Without it, the tool counts as a write.
- **No shims.** When an API changes, its callers change with it. A second set of fields kept "for tests" is a finding.
- **No knobs.** A fixed value is a constant with a comment saying why that value, not an option.
- **Dependencies.** A new one needs a reason in DESIGN.md.

## Errors and outputs

- An error an agent will read says what to do next: "re-read it", "use a shorter range", "run: pim-mcp account add".
- Sentinel errors where callers branch on them, wrapped with `%w` and context.
- A changed tool name, argument or output field is reflected in the tool's description, the README table and DESIGN.md in the same change.
- The README states nothing that the code doesn't do.

## Tests

- A bug in parsing, expansion or import is fixed test-first, with the input that showed it.
- A test asserts something a regression would break. A test that only calls a function is noise.
- An existing test is changed only when the behaviour it pins was changed on purpose, and the change says so.
- An integration test finds its own data by a name unique to the run, so the suite passes twice against the same servers.

## Comments and prose

- A comment says why, in a short plain sentence. What the code does is the code's job.
- Docs and messages use the README's voice: plain words, short sentences, no marketing.
