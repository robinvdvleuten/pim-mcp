# Design decisions

This file records the decisions that shape pim-mcp, and the findings that forced them. The README says what pim-mcp does. This says why it does it that way, and what that costs.

Each record has a decision, the reason for it, and its consequence. If you want to reverse one, start from the reason.

## CalDAV is spoken with net/http and encoding/xml

**Decision.** `internal/calendar` sends its own requests. It does not use go-webdav.

**Why.** Three limits in go-webdav showed up in turn. Its `PutCalendarObject` can't send `If-Match` or `If-None-Match`, and it hides the status code in an internal error type, so a `412` can't be told from any other failure. Its `FindCalendars` doesn't return a calendar's colour or the user's privileges. Its `QueryCalendar` fails as a whole if one stored event is malformed, so one bad event hides the rest of a calendar.

**Consequence.** We own a small amount of WebDAV: PROPFIND, REPORT, PUT, GET and DELETE, and the XML for them. ETags are kept unquoted inside the program and quoted again on the wire. Responses over 16 MiB are an error.

## IMAP uses go-imap v2 beta, one connection per tool call

**Decision.** Mail uses `go-imap/v2` at a beta release. Every tool call opens its own connection, logs in, does its work and logs out.

**Why.** v2 handled login, listing, search and MIME parts against Dovecot, and v1 has had no release since 2022. v2's commands take no context. So when a context ends, the connection is closed, which is the only way to stop a command that is waiting on the server. A closed connection can't be used again. IMAP also keeps one selected folder per connection, so parallel tool calls on a shared connection would interfere.

**Consequence.** Each mail call pays for a TLS handshake and a login. A call that times out, after a minute, drops its connection and the error says the context ended, not that the connection closed. The beta API may still change, so upgrades need testing against Dovecot. It has not been run against more than one real provider.

## Every write is conditional

**Decision.** Creating an event sends `If-None-Match: *`. Updating or deleting sends `If-Match` with the ETag the caller read. A write without an ETag is refused before it is sent. A `412` becomes `ErrConflict`, and the server's copy is not touched.

**Why.** The agent works from a copy it read some time ago, and the user may have edited the event on a phone since. A blind PUT would overwrite that edit. Updates also fetch the stored event first and compare its ETag, so the change is computed from the server's current copy and a stale caller finds out before any work is done.

**Consequence.** An agent has to read an event before it can change it, and has to re-read after a conflict. There is no "force" option. Two writes that race still resolve safely, because the server decides on the precondition.

## Weak ETags, and a PUT that returns no ETag

**Decision.** A weak marker (`W/`) is stripped when an ETag is read, and the ETag is then sent back as a strong one in `If-Match`. When a PUT succeeds but the response has no ETag, the client reads the resource back and uses its ETag.

**Why.** Servers differ in what they send. A server that gives weak ETags will not match a strong `If-Match`, so the write fails with a conflict. That is the safe outcome: it reports a conflict instead of overwriting. RFC 4791 lets a server leave out the ETag when it changed the data it stored, so the read-back is the only way to learn the new version.

**Consequence.** On a server that only sends weak ETags, a strict `If-Match` check means updates and deletes report conflicts instead of succeeding. The read-back after a PUT is a separate request, so if someone else edits the event between the two, the ETag we report is theirs, not ours.

## Schedule-Reply: F on every PUT and DELETE

**Decision.** Every PUT and DELETE sends `Schedule-Reply: F` (RFC 6638), not only imports.

**Why.** pim-mcp never sends mail, so no server should send replies on its behalf. A test on Baïkal showed a plain PUT delivering a reply to the organiser, and the same PUT with the header not doing so.

**Consequence.** The header only suppresses replies. On a server with implicit scheduling, changing or deleting an event the user organises can still make the server notify its attendees. Nothing in pim-mcp can prevent that, so the README lists it under limits. pim-mcp also never replies to an invite on the user's behalf.

## Calendar and event IDs are server paths

**Decision.** A calendar's `id` is its path on the server, and an event's `event_id` is the path of its resource. A calendar home set, or any href, that names another host is refused.

**Why.** Paths need no mapping table, survive restarts, and are what the server needs to be given. Every request goes to the one configured host with the user's password. A home set that pointed elsewhere would lead the password there.

**Consequence.** IDs are long and meaningless to a person. Providers that put the calendar home on another host can't be used. iCloud does this, so iCloud doesn't work.

## IDs from the agent must lie inside the calendar home

**Decision.** Every calendar ID and event ID passed into `internal/calendar` is checked against the user's calendar home before any request is made for it. A calendar must be below the home, and an event below a calendar. The path is cleaned first, and the cleaned path is the one sent. Anything else fails with `ErrOutsideCalendars`.

**Why.** IDs are paths, and they come from an agent that a mail may have misled. On a server such as Nextcloud the same host and password also reach files and contacts, so an unchecked path would let a calendar tool write there.

**Consequence.** The home has to be known before the first read or write of an event, which costs two lookups per client. The server keeps one client for each account, and with it its connections and the home, for as long as that account's CalDAV URL, username and password read at each call stay the same. A changed account or password gets a new client. A call that waits for the lookup gives up when its own context ends. A calendar the server lists outside the home is left out of listings, since no write to it would be accepted. On a server whose home also holds other collections, such as Radicale's address books, the check doesn't tell those from calendars. The server refuses calendar data there itself.

## Time zones

**Decision.** A TZID is resolved in this order: the CLDR Windows zone table, then IANA names, then the VTIMEZONE embedded in the same calendar. A TZID that none of these knows is an error. It is never guessed. tzatziki does the resolving (see "Discovery and time zones are libraries"). Zone data is embedded with `time/tzdata`, which pim-mcp imports itself, because tzatziki leaves that choice to the program.

**Why.** Real invites carry Windows names such as "W. Europe Standard Time", IANA names, and libical-style prefixed names. A name is more reliable than the sender's embedded definition, because the definition may be out of date. An unknown zone turned into a guess would put an event at the wrong hour without any sign of it. The binary is meant to run in a container with no zone files, so it carries its own.

**Consequence.** A calendar with a zone nobody can resolve fails to expand, and is reported as skipped when listing. Embedded definitions are turned into real zones, with a bound on how many rule steps one observance may take, since they come from strangers.

## Floating times, all-day events and the user's zone

**Decision.** Floating times and all-day dates are read in the user's zone, which is passed in rather than read from global state. The zone comes from `TZ`, then the `/etc/localtime` link, then Go's unnamed local zone. An empty `TZ` means UTC. A `TZ` that is an absolute path falls back to Go's local zone. The reported `timezone` is empty when no IANA name is known.

**Why.** Go can't tell us the name of its local zone, only that it is "Local". Without the name, we couldn't give an agent an IANA zone to repeat back to us. A floating time means "this wall-clock time wherever I am", so the user's zone is the only sensible reading. A date typed without a time, such as a `since` or `start`, is read in the same zone, and `account_info` reports its name.

**Consequence.** If the server runs in a container with no `TZ`, the user's zone is UTC and all-day events begin at UTC midnight. The user's zone is also not a setting of its own: to change it you change the environment.

## Event listing

**Decision.** A listing asks the server for a range one day wider on each side, then expands exactly. It makes a second query for every event that has a `RECURRENCE-ID`, whatever its dates. A stored event that can't be read is skipped and named in `skipped` with a reason. Too many occurrences is an error.

**Why.** A server reads floating times in a zone of its own, so an event near the edge of the range can be missed. Nextcloud's range filter goes by the series, so an occurrence that was moved outside the series' dates is not found by the first query. A single unreadable event, such as one with an unknown zone, should not hide the rest of a calendar. But it must not vanish silently either, so it is reported.

**Consequence.** A listing costs two REPORT requests per calendar, and fetches events it later drops. The two requests of a calendar run together, and so do up to 4 calendars, so a listing across many calendars takes about as long as its slowest few. Results are joined in the order the calendars were given, so `skipped` does not depend on which answer came first. The first failure, in that order, is the error, and later requests are cancelled. The 1000-occurrence cap is applied to each expansion and again across calendars. Free/busy is computed from the same listing, so it shares both the skipped list and the cost.

## Created events are stored in UTC, recurring ones in their zone

**Decision.** A single timed event that pim-mcp creates is written in UTC. A local time without a zone or offset is refused. A recurring timed event is written in the IANA zone the agent names, as `DTSTART;TZID=` and `DTEND;TZID=`, together with a `VTIMEZONE` for that zone, and is refused without a `timezone`. An RFC 3339 start is converted into that zone first. A recurring all-day event has dates and no zone. The rule is an RRULE from the agent, checked with rrule-go and written back from the parsed options. It is refused if it repeats more often than daily, has both `COUNT` and `UNTIL`, has no occurrence, or does not have the start as its first occurrence. `UNTIL` is stored as a UTC date-time for a timed event, and as a date for an all-day one.

**Why.** A single event with no recurrence needs only the instant, so naming a zone would only add a definition to carry. Guessing a zone for a bare local time puts the event at the wrong hour if the guess is wrong. A recurring event is different: "every Monday at 09:00" has to stay at 09:00 when the clocks change, which a fixed UTC time can't do. RFC 5545 requires a `VTIMEZONE` for every `TZID`, so tzatziki generates it from Go's own zone data, as one observance for each real clock change from 1 January of the start's year through ten years after it. That avoids inferring rules, and a reader that only has this definition gets the right offset at every moment it covers. A start that the rule would not pick is refused, because RFC 5545 says it should, and otherwise the first occurrence listed would not be the one asked for.

**Consequence.** Other clients show a single event in the viewer's zone, but can't tell which zone the user meant. A recurring event does carry it. After the ten years the definition ends and a client that only has it keeps the last offset, so a series longer than that drifts by an hour in such a client. pim-mcp is not affected, since it reads an IANA name from its own data before it looks at the definition. The definition is not updated later, so editing the series does not extend it. Changing a stored event keeps the zone of the property it replaces, so an imported event with a named zone keeps it. An edit also raises `SEQUENCE` and sets `LAST-MODIFIED`, which makes a later re-import of the original invite a no-op.

## Read-only mode is one function

**Decision.** Every tool is registered through `addTool`. In read-only mode it skips any tool whose annotations don't say `ReadOnlyHint`. A tool with no annotation counts as a write. `--tools` narrows the set further, and an unknown name is an error. `PIM_MCP_READ_ONLY` must be a value `strconv.ParseBool` reads, or the server won't start.

**Why.** A read-only switch that has to be remembered in each new tool will be forgotten in one. Making the safe reading the default means a mistake hides a tool instead of exposing it. A typo in a safety setting should stop the server, not turn the setting off.

**Consequence.** The write tools are absent from `tools/list`, so the agent can't call them and can't be told by a mail to call them. A new read-only tool has to say so on purpose. `calendar_preview_import` is the example: it is read-only, so it stays available.

## Text written to a calendar is escaped

**Decision.** Everything we write, whether it comes from the agent or from an invite, goes through the escaping of the iCalendar library. Carriage returns are turned into newlines first. Text from an invite is unescaped, then escaped again.

**Why.** Radicale cuts a text value at a comma that isn't escaped. Real invites do contain bare commas, so a summary with a comma in it came back cut short. Separately, an unescaped line break inside a description would start a new line that a reader takes for a property, which lets a stranger add properties to an event.

**Consequence.** The text a reader gets back is what was written. Properties that hold a list, such as `CATEGORIES`, are left alone, since their commas mean something.

## Import

**Decision.** An invite is matched to a stored event by UID. For a request or publish, a higher `SEQUENCE` updates, an equal one is skipped as already imported, and a lower one is skipped as older. A cancellation is skipped when its `SEQUENCE` is lower than the stored one, or when the event isn't there. New events are stored at a path made from a hash of the UID. The plan is worked out again when the import runs, not taken from the preview.

**Why.** RFC 5546 says a higher `SEQUENCE` is a newer version. A cancellation is sent for the version it cancels, so it doesn't need a higher one, but it shouldn't undo a change made later. UIDs come from strangers and can hold a slash or anything else, so they can't be used as file names. A preview is only a guess about a moment that has passed. Planning again at import time means importing twice changes nothing the second time, and a stored event that changed in between fails its precondition instead of being overwritten.

**Consequence.** Each event of an invite is looked up once, and the write that follows is conditional on the ETag that lookup returned. If the event is changed or deleted in between, the write fails with a conflict (or, on a server that answers 404, "not found"). A cancellation whose event has vanished is skipped as already gone. An invite with no UID gets one made from its start, end, summary, location and organiser, and not from its timestamp, so the same invite still matches itself. An invite for a single occurrence is merged into the stored series instead of replacing it. A cancellation of an occurrence that is already gone is not an error. Only `REQUEST`, `PUBLISH` and `CANCEL` are imported. `METHOD` is removed before storing, because CalDAV forbids it. Windows zone names are replaced with IANA ones. Nothing replies to the organiser. Events of one invite are independent, so one failing does not stop the others. If none was written, the first error is the tool's error. If some were, the result is returned with `failed` set and an `error` on each event that was not written, because the agent has to be told what changed. Once the context has ended, the remaining events are marked failed without being tried.

## Limits, and nothing is cut silently

**Decision.** A request that would go over a limit fails with an error that says what the limit is. The exception is where cutting is the point, and then the result says it was cut. Limits: ranges of 366 days; 1000 occurrences per query; messages of 25 MiB; calendar parts of 1 MiB; message bodies of 64 KiB, with `body_truncated` set; CalDAV responses of 16 MiB; mail searches that must read messages stop after 2000; search results of 50, with `more` set; recurrence rules are bounded in steps and in age.

**Why.** An agent that is handed part of a list as if it were the whole will give wrong answers with confidence. Free/busy computed from a cut list would claim time is free. Invites come from strangers, so the rules that expand them must also be bounded in cost. The libraries that parse calendar data and expand rules panic on some malformed input, which fuzzing found, so a panic there is caught and returned as an error like any other.

**Consequence.** Callers sometimes have to ask again with a shorter range. Searches by calendar part or exact date can't be answered by the server, so they read messages in batches of 100, which is why the scan limit exists.

## Mail is read-only by construction

**Decision.** No tool sends, moves, flags or deletes mail. Folders are opened with `EXAMINE` (read-only), and message parts are fetched with `BODY.PEEK`, so reading doesn't set the seen flag. Message IDs carry the folder name, the UIDVALIDITY and the UID. An ID whose UIDVALIDITY no longer matches is refused.

**Why.** The agent reads mail written by strangers, so the safest mail tools are the ones that can't change anything. Read-only at the protocol level also protects against bugs in our own code. UIDs are only stable while UIDVALIDITY holds. After a folder is recreated, an old UID may name a different message.

**Consequence.** Search narrows by the server's criteria and widens date bounds by a day, because IMAP compares dates only, in each message's own zone. Exact dates are checked after reading. Where the server supports `SORT`, results come newest first; where it doesn't, a page may not hold the newest messages. Anything that needs mail to change is out of scope.

## HTML mail is converted to text

**Decision.** When a message has no plain-text part, `mail_get` turns the HTML into text in `internal/mail` and sets `body_converted`. `body_type` stays the media type the text came from, so it still says `text/html`. Scripts, styles, the head, comments and elements hidden with the `hidden` attribute or an inline `display:none` or `visibility:hidden` are dropped. Block elements and `<br>` become line breaks, and a link's target is kept as `text (url)` when it differs from the text. Only `http`, `https`, `mailto` and `tel` targets are kept. The conversion uses the tokenizer of `golang.org/x/net/html`, which is the one dependency added for it. The same conversion applies to a part read with `mail_get`'s `part` argument, which reads only `text/*` and calendar parts.

**Why.** Raw HTML costs the agent many tokens, and it can carry text that the person reading the mail never sees. Parsing HTML by hand is the wrong tool for hostile input, and the tokenizer keeps no tree, so deeply nested markup costs no stack. It runs in linear time, and the 25 MiB message limit bounds the input.

**Consequence.** The agent sees what a reader would, not what the sender wrote. Tricks that need rendering, such as white text on a white background, or styles from a stylesheet, are not caught. The 64 KiB cut applies after conversion. An unclosed hidden element hides everything after it, as in a browser.

## Passwords, and the confirmation before they are sent

**Decision.** The password is kept in the OS keychain, under the service name `pim-mcp` and the email address. The config file holds endpoints and usernames only, in a `0600` file in a `0700` directory, replaced in one step when saved. There is no password flag. `PIM_MCP_PASSWORD` wins over the keychain, which is then not touched, but only while one account is configured (see "Several accounts"). A CalDAV URL that contains a password is refused. When `account add` finds servers by discovery, it prints them and asks `[y/N]` before it saves anything. If both servers are given as flags, there is nothing to confirm and it doesn't ask.

**Why.** Discovery answers come from DNS and websites, which an attacker may control. If they are trusted, the password goes wherever an attacker said. Flags land in shell history and in the process list. A URL with a password in it would be shown by `account_info`.

**Consequence.** Without a terminal, `account add` can't confirm, so you pass `--imap` and `--caldav` yourself. Discovery is hearsay's, and the confirmation is pim-mcp's, as hearsay asks of its callers. Nothing is sent during discovery except lookups: Mozilla's ISPDB is asked for the domain only, never the full address, and `--no-ispdb` turns it off. The environment variable is visible to anything that can read the process's environment, and it is no help to someone with several accounts.

## Several accounts

**Decision.** The config file holds a list of accounts, and the first is the default. `account add` appends, so the first account stays the default until `account default` moves another to the front, and it refuses a second account with the same email address in any letter case. Every tool takes an optional `account`, the email address, in one embedded type, `AccountArg`. Left out, it means the default. An unknown address is an error that lists the configured ones. The accounts are read on every call, and the choice is made in one place, `Server.resolve`, which hands the dial functions an address that is configured. Mail connections are opened per call, as before. Calendar clients are kept per email address, and forgotten once their account is gone. `PIM_MCP_PASSWORD` is used only while one account is configured. With several, any attempt to get a password while it is set is an error, and so is adding a second account while it is set. The rule is in `internal/account`.

**Why.** IDs are server paths and IMAP UIDs, and they stay what the server gave. Putting the account inside them would change both formats and the checks built on them, and the agent names the account in each call anyway, so it passes the same `account` it got an ID from. The environment variable is one password. With several accounts, sending it to whichever account was asked for would hand one server's password to another, and falling back to the keychain would hide that the variable was ignored. Failing loudly is the only reading that can't leak and can't surprise.

**Consequence.** A wrong pairing of ID and account mostly fails, but it is not fail-closed everywhere. A calendar ID is checked against the calendar home of the account it is used with, so an ID from a server with another home is refused with `ErrOutsideCalendars`, before a request goes out for it. If two servers happen to put calendars at the same path, which can happen with the same user name on two Baïkal servers, the ID passes, and a create or a list reaches the other account's calendar of that name. An update or delete also needs the event's ETag, so it fails with a conflict or "not found" there. A message ID carries the folder and UIDVALIDITY, so one from another account is refused when the folder doesn't exist there or has another UIDVALIDITY. UIDVALIDITY is only a number, set when a folder is created, and some servers hand out small or constant ones, so two accounts can agree. Then the ID names whatever message has that UID in the other account, and it is read. Nothing is written in that case, and both accounts are the user's, but the agent can be told about the wrong mail. The same goes for an import, which can then add an event from the wrong mail. A dial for one account never uses another's address, user name, password or client, and the tests check it. With several accounts, `PIM_MCP_PASSWORD` can't be used at all, so Docker and CI setups that rely on it are limited to one account. Two `account add` commands run at the same moment can lose one of the accounts, as the file is replaced and not locked.

## Transport

**Decision.** IMAP uses implicit TLS or required STARTTLS. CalDAV uses HTTPS. Certificates are always verified, and there is no switch to turn that off. Plain IMAP and plain HTTP are allowed to loopback addresses only, for tests. A redirect is held to the same rule.

**Why.** Go's HTTP client keeps the credentials when a redirect only changes the scheme, so a redirect to plain HTTP would send the password in the clear. The loopback exception is what lets the integration tests run against containers.

**Consequence.** A self-hosted server with a self-signed certificate must have its certificate trusted by the machine. The rule lives in one package, `internal/transport`, so that mail, calendar and config checks cannot disagree. Discovery runs in hearsay, which has its own copy of the same rule, so a change to it has to be made in both.

## Discovery and time zones are libraries

**Decision.** Server discovery is [hearsay](https://github.com/robinvdvleuten/hearsay), and VTIMEZONE reading, writing and TZID resolution are [tzatziki](https://github.com/robinvdvleuten/tzatziki). Both were written here and moved out, and both belong to this project's author.

**Why.** Neither is about MCP, and no other Go package did either job: other programs that read invites or set up mail accounts need the same code. On their own, each has its own tests, fuzzing and releases, and pim-mcp keeps only what is about mail, calendars and agents.

**Consequence.** A fix to discovery or time zones is released there first, then taken in here with a version bump, which Dependabot proposes. Until they reach 1.0, a release may change their API, and pim-mcp changes with it. Their errors name the library, as in `tzatziki: unknown time zone`, and that text can reach an agent.

## Logging

**Decision.** Logs go to stderr, as structured records, and never to stdout. At debug level, which `PIM_MCP_DEBUG` turns on, each request logs the method, the tool, how long it took, and whether it failed. Arguments and results are never logged.

**Why.** Stdout carries the MCP protocol, so a stray line there breaks the session. Arguments and results are people's mail and calendars. A test runs the real binary and checks its logs for the password and message content.

**Consequence.** Debugging a wrong answer means reproducing it. The log shows that a call failed, but not what it was asked.
