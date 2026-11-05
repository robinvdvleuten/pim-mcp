# pim-mcp

Lets an AI agent read your mail and manage your calendar on your own servers, with one login. The agent can find the meeting in an email and put it in your calendar in one step, with recurrence and time zones intact. It's built on the assumption that agents make mistakes and that invites come from strangers: your password stays in your keychain, your mail can't be changed, and a stale write can't overwrite an edit you just made on your phone.

- **One account.** Enter your email address and password, and it finds your IMAP and CalDAV servers itself.
- **Password in your keychain.** Never in a config file, never in logs, and never sent anywhere until you've confirmed the servers.
- **Email to calendar.** It previews what an invite would do, then imports it. Importing twice changes nothing, and a newer version of an invite updates the event.
- **Encrypted, verified connections.** Certificates are always checked, and there's no switch to turn that off.
- **Safe writes.** Every change is conditional on the version the agent read. If the event changed since, the write fails and your copy stays.
- **Read-only mode.** `--read-only` removes every tool that can change something, so the agent can't call them at all.
- **Mail can't be changed.** No tool sends, moves or deletes mail, and reading leaves it unread.
- **Self-hosted first.** Integration tests run against Dovecot, Radicale, Baïkal and Nextcloud, with 38 calendar test files.
- **One binary.** Written in Go, about 16 MB, with time zone data built in.

```text
You:    Add the meeting from Jan's last email to my calendar.

Agent:  mail_search             → "Project kickoff", from Jan, has a calendar invite
        calendar_preview_import → create "Kickoff, phase 1", 15 Jan 09:00–10:00,
                                  weekly, overlaps nothing
        "This adds a weekly meeting from 15 January. Shall I?"

You:    Yes.

Agent:  calendar_import_from_email → created
```

> **Early days.** Reading mail and calendars has been tried on one real provider. Creating, changing and importing events have only run against the test servers above, so start with `--read-only`.

---

## Getting started

You need [Go](https://go.dev/dl/), which downloads the exact version this project needs by itself, and an MCP client such as Claude Code.

**1. Build it:**

```bash
git clone https://github.com/robinvdvleuten/pim-mcp.git
```

```bash
cd pim-mcp
```

```bash
go build -o pim-mcp ./cmd/pim-mcp
```

**2. Add your account.** It shows the servers it found and asks before your password goes there. If your provider has app passwords, use one.

```bash
./pim-mcp account add --email you@example.org
```

**3. Check that it can log in:**

```bash
./pim-mcp account test
```

```text
imap:   ok
caldav: ok
```

**4. Connect your MCP client,** read-only to begin with. For Claude Code:

```bash
claude mcp add pim -- "$(pwd)/pim-mcp" serve --read-only
```

**5. Ask your agent** something like "What's on my calendar next week?"

When you're happy with what it does, add the server again without `--read-only` to allow changes.

---

## Tools

| Tool | What it does | Read-only mode |
|---|---|---|
| `account_info` | Shows the account and its servers | ✓ |
| `mail_list_folders` | Lists mail folders and their roles | ✓ |
| `mail_search` | Searches a folder by sender, subject, text, date, or whether it holds an invite | ✓ |
| `mail_get` | Reads a message: headers, text and a list of its parts | ✓ |
| `calendar_list_calendars` | Lists calendars | ✓ |
| `calendar_list_events` | Lists events in a range, with recurring events expanded | ✓ |
| `calendar_free_busy` | Finds when you're busy | ✓ |
| `calendar_preview_import` | Shows what importing an invite would do, and writes nothing | ✓ |
| `calendar_create_event` | Creates a single event | |
| `calendar_update_event` | Changes an event, or one occurrence of a recurring one | |
| `calendar_delete_event` | Deletes an event, or one occurrence of a recurring one | |
| `calendar_import_from_email` | Adds, updates or cancels an event from an invite | |

Mail is always read-only: no tool sends, moves, flags or deletes a message, and reading leaves it unread.

---

## Security

**Your password**

- It's stored in the OS keychain. The config file holds only endpoints and usernames, with owner-only permissions (`0600` in a `0700` directory).
- There's no `--password` flag, so it can't end up in your shell history or process list. A CalDAV URL containing a password is refused.
- During setup, nothing is sent until you've seen the servers it found and said yes, because those answers come from DNS and websites.
- Debug logs record which tool ran and how long it took, never arguments or results. A test runs the real binary and checks the logs for the password and message content.

**Your connections**

- IMAP uses TLS or required STARTTLS, and CalDAV uses HTTPS. Certificates are always verified.
- Unencrypted connections are only allowed to this machine, for testing.
- A redirect to plain HTTP is refused, as is a calendar server that points to another host, so your password can't be led elsewhere.

**Your data**

- Folders are opened read-only and messages read without marking them seen, so mail can't change even by accident.
- Every calendar write names the version it was based on. A test edits an event from a second client mid-way and checks that the first write fails and nothing is lost.
- Asking to delete one occurrence can't delete a whole series: scope and occurrence have to agree, or the call fails.
- Any new tool counts as a write unless it says otherwise, so read-only mode can't miss one.

**What strangers send you**

Invites and mail are treated as hostile input:

- Text is escaped before it's stored, so a summary or description can't smuggle extra properties into an event.
- Recurrence rules, time zone definitions, message sizes and server responses all have limits, to keep a bad invite from tying up the machine.
- Tool descriptions tell the agent to treat mail and invites as information, not instructions.

**What it can't protect you from**

> **Prompt injection is still possible.** An email can try to talk your agent into something, and a warning in a tool description is no guarantee. An agent with these tools can read all your mail, and, unless read-only, change or delete calendar events. Your defences are `--read-only`, `--tools`, and an MCP client that asks before calling tools that write.

- **Anything your agent reads goes to your AI provider,** and to any other tools the agent has.
- **The password opens your whole account.** Use an app password if your provider has them.
- **`PIM_MCP_PASSWORD`** is visible to anything that can read the process's environment. Use the keychain where you can.
- **No independent security audit** has been done.

---

## Configuration

**Finding servers.** `account add` tries built-in settings for mailbox.org, Fastmail and Posteo, then DNS, then the well-known locations providers publish. To skip this, name the servers yourself:

```bash
./pim-mcp account add --email you@example.org --username you --imap mail.example.org:993 --caldav https://dav.example.org/
```

One lookup, Mozilla's ISPDB, tells Mozilla your email domain. `--no-ispdb` turns it off. Your full address is never sent during discovery.

**Choosing tools.** `serve --tools mail_search,mail_get` offers only those tools. Read-only mode still applies on top.

**Environment variables:**

| Variable | Effect |
|---|---|
| `PIM_MCP_PASSWORD` | Uses this password instead of the keychain, for Docker and CI |
| `PIM_MCP_READ_ONLY=1` | Same as `--read-only` |
| `PIM_MCP_DEBUG=1` | Logs each request to stderr: the tool and how long it took, never its arguments or results |

Other commands are `account show` and `account remove`.

---

## Limits

- **One account** per installation.
- **No replying to invites.** Importing adds the event to your calendar and tells nobody. On a server that handles scheduling itself, changing an event you organize can make that server notify its attendees.
- **No Google or Microsoft sign-in.** Only servers that accept a username and password work.
- **Recurring events** can't be created directly, only imported. Editing covers a whole series or a single occurrence, not "this and following".
- **Searches** that have to read messages, such as for invites, stop after 2000 and say that there may be more.
- **Ranges and sizes.** Event listings cover at most 366 days and 1000 occurrences. Messages over 25 MiB aren't read.

---

## Development

```bash
go test -race ./...
```

Integration tests need Docker:

```bash
docker compose -f test/integration/compose.yaml up -d --wait
```

```bash
go test -race -tags integration ./test/integration/...
```

Add `--profile nextcloud` to the compose command and set `PIM_MCP_TEST_NEXTCLOUD=1` to include Nextcloud.

To lint commit messages and code, install [mise](https://mise.jdx.dev) and run:

```bash
mise install && mise run setup
```
