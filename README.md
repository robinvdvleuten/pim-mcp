# pim-mcp

PIM is short for personal information manager, the kind of app that keeps your mail and calendar together. pim-mcp lets an AI agent read your mail and manage your calendar on your own servers, with one login. The agent can find the meeting in an email and put it in your calendar in one step, with recurrence and time zones intact. It's built on the assumption that agents make mistakes and that invites come from strangers: your password stays in your keychain, your mail can't be changed, and a stale write can't overwrite an edit you just made on your phone.

- **One login per account.** Enter your email address and password, and it finds your IMAP and CalDAV servers itself. Add more than one account if you like: the first is the default, and the agent can name another.
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

You need an MCP client such as Claude Code.

**1. Install it** with [Homebrew](https://brew.sh):

```bash
brew install --cask robinvdvleuten/tap/pim-mcp
```

Each [release](https://github.com/robinvdvleuten/pim-mcp/releases/latest) also has archives for macOS, Linux and Windows, and `.deb`, `.rpm` and `.apk` packages. To run it in Docker instead, see [Docker](#docker).

**2. Add your account.** It shows the servers it found and asks before your password goes there. If your provider has app passwords, use one. To add another account later, run the same command with its address. The first account you add is the default.

```bash
pim-mcp account add --email you@example.org
```

**3. Check that it can log in:**

```bash
pim-mcp account test
```

```text
imap:   ok
caldav: ok
```

**4. Connect your MCP client,** read-only to begin with. For Claude Code:

```bash
claude mcp add pim -- "$(command -v pim-mcp)" serve --read-only
```

**5. Ask your agent** something like "What's on my calendar next week?"

When you're happy with what it does, add the server again without `--read-only` to allow changes.

### Docker

The image is `ghcr.io/robinvdvleuten/pim-mcp`. A container has no keychain, so the password comes from `PIM_MCP_PASSWORD`, which limits you to one account. The account settings live in a folder on your machine that each container mounts.

Put the password in your shell without it going into your history:

```bash
read -rs PIM_MCP_PASSWORD && export PIM_MCP_PASSWORD
```

Add the account and check it:

```bash
docker run -it --rm -e PIM_MCP_PASSWORD -v "$HOME/.config/pim-mcp:/home/nonroot/.config/pim-mcp" ghcr.io/robinvdvleuten/pim-mcp account add --email you@example.org
```

```bash
docker run --rm -e PIM_MCP_PASSWORD -v "$HOME/.config/pim-mcp:/home/nonroot/.config/pim-mcp" ghcr.io/robinvdvleuten/pim-mcp account test
```

Connect Claude Code. This stores the password in Claude Code's own settings:

```bash
claude mcp add pim -e PIM_MCP_PASSWORD="$PIM_MCP_PASSWORD" -- docker run -i --rm -e PIM_MCP_PASSWORD -v "$HOME/.config/pim-mcp:/home/nonroot/.config/pim-mcp" ghcr.io/robinvdvleuten/pim-mcp serve --read-only
```

The container runs as user 65532. On Linux without Docker Desktop, that user needs to own the folder: `sudo chown 65532 ~/.config/pim-mcp`.

### From source

With [Go](https://go.dev/dl/), which downloads the exact version this project needs by itself:

```bash
go install github.com/robinvdvleuten/pim-mcp/cmd/pim-mcp@latest
```

---

## Tools

| Tool | What it does | Read-only mode |
|---|---|---|
| `account_info` | Shows an account, its servers and your time zone, and lists your accounts | ✓ |
| `mail_list_folders` | Lists mail folders and their roles | ✓ |
| `mail_search` | Searches a folder by sender, subject, text, date, or whether it holds an invite | ✓ |
| `mail_get` | Reads a message: headers, text (HTML is turned into text) and a list of its parts, or the text of one part | ✓ |
| `calendar_list_calendars` | Lists calendars | ✓ |
| `calendar_list_events` | Lists events in a range, with recurring events expanded | ✓ |
| `calendar_free_busy` | Finds when you're busy | ✓ |
| `calendar_preview_import` | Shows what importing an invite would do, and writes nothing | ✓ |
| `calendar_create_event` | Creates an event, optionally recurring from an RRULE | |
| `calendar_update_event` | Changes an event, or one occurrence of a recurring one | |
| `calendar_delete_event` | Deletes an event, or one occurrence of a recurring one | |
| `calendar_import_from_email` | Adds, updates or cancels an event from an invite | |

Mail is always read-only: no tool sends, moves, flags or deletes a message, and reading leaves it unread.

Every tool takes an optional `account`, the email address of the account to use. Leave it out and the default account is used. An id from `mail_search`, `calendar_list_calendars` or `calendar_list_events` belongs to the account that returned it, so the agent passes the same `account` again.

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
- **`PIM_MCP_PASSWORD`** is visible to anything that can read the process's environment. Use the keychain where you can. It is one password, so it only works with a single account: with several, pim-mcp refuses to use it rather than send it to every account's servers.
- **No independent security audit** has been done.

---

## Configuration

**Finding servers.** `account add` tries built-in settings for mailbox.org, Fastmail and Posteo, then DNS, then the well-known locations providers publish. To skip this, name the servers yourself:

```bash
pim-mcp account add --email you@example.org --username you --imap mail.example.org:993 --caldav https://dav.example.org/
```

One lookup, Mozilla's ISPDB, tells Mozilla your email domain. `--no-ispdb` turns it off. Your full address is never sent during discovery.

**Choosing tools.** `serve --tools mail_search,mail_get` offers only those tools. Read-only mode still applies on top.

**Environment variables:**

| Variable | Effect |
|---|---|
| `PIM_MCP_PASSWORD` | Uses this password instead of the keychain, for Docker and CI. Only works with a single account; with several it is an error |
| `PIM_MCP_READ_ONLY=1` | Same as `--read-only` |
| `PIM_MCP_DEBUG=1` | Logs each request to stderr: the tool and how long it took, never its arguments or results |

Other commands are `account show`, which lists the accounts and marks the default, `account test [--email ADDRESS]`, `account remove [--email ADDRESS]` (`--email` is needed when you have several) and `account default --email ADDRESS`, which makes an account the default.

---

## Limits

- **Ids belong to one account.** The agent has to pass the same `account` that gave it a message, calendar or event id. A wrong pairing is usually refused, but that is not guaranteed, so it can read or list the wrong account.
- **No replying to invites.** Importing adds the event to your calendar and tells nobody. On a server that handles scheduling itself, changing an event you organize can make that server notify its attendees.
- **No Google or Microsoft sign-in.** Only servers that accept a username and password work.
- **Recurring events** can be created, from an RRULE such as `FREQ=WEEKLY;BYDAY=MO`, and a timed one needs a time zone. Editing covers a whole series or a single occurrence, not "this and following", and a series' rule can't be changed after it is created.
- **Searches** that have to read messages, such as for invites, stop after 2000 and say that there may be more.
- **Ranges and sizes.** Event listings cover at most 366 days and 1000 occurrences. Messages over 25 MiB aren't read.

---

## Development

Install [mise](https://mise.jdx.dev), which fetches the pinned Go, linter and Vale, then set up the commit hook:

```bash
mise install && mise run setup
```

Check a change: formatting, unit tests, lint and known vulnerabilities.

```bash
mise run check
```

Integration tests need Docker. This starts fresh test servers, runs the suite and removes them:

```bash
mise run integration
```

Set `PIM_MCP_TEST_NEXTCLOUD=1` to include Nextcloud, and `PIM_MCP_TEST_STACK=1` to run a second stack beside the first on other ports.

The reasoning behind the design is in [DESIGN.md](DESIGN.md), and what a review looks for is in [CODING_STANDARDS.md](CODING_STANDARDS.md).

## License

MIT © [Robin van der Vleuten](https://robinvdvleuten.nl). See [LICENSE](LICENSE) for more information.
