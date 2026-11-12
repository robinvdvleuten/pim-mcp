# Changelog

## 1.0.0 (2026-11-12)


### Features

* add account add, show and remove commands ([34a5af7](https://github.com/robinvdvleuten/pim-mcp/commit/34a5af7039a80b211e46d1e684a15d7692c0d6d0))
* add account test and the account_info tool ([1d34b4e](https://github.com/robinvdvleuten/pim-mcp/commit/1d34b4ecfc4428bbd19de65bbfe213671b1d08fd))
* add calendar_create_event and read-only gating ([485bb28](https://github.com/robinvdvleuten/pim-mcp/commit/485bb28ede6b86c704a19a6d99dc1616d2b21645))
* add calendar_free_busy tool ([4901c26](https://github.com/robinvdvleuten/pim-mcp/commit/4901c265838dc518d063eabe96cd06b537daf996))
* add calendar_list_calendars tool ([5575377](https://github.com/robinvdvleuten/pim-mcp/commit/55753774c0fcc3db504f5b81243c1b53eacaa1fc))
* add calendar_list_events tool ([1ab3d81](https://github.com/robinvdvleuten/pim-mcp/commit/1ab3d815a125ab7e7864a3e735718250e85cb9f5))
* add calendar_update_event and calendar_delete_event ([4a715c7](https://github.com/robinvdvleuten/pim-mcp/commit/4a715c7b8d447c7ced6f0d4790100d2f9d05d53b))
* add debug logging, response limits and a Nextcloud test setup ([9f8338e](https://github.com/robinvdvleuten/pim-mcp/commit/9f8338eee622aa3717ab461dc1b1ae0d10b0a6af))
* add mail_get tool ([209966f](https://github.com/robinvdvleuten/pim-mcp/commit/209966f62b6ce4e568f163124ddfacb142d4e96e))
* add mail_list_folders and mail_search tools ([5dddcfc](https://github.com/robinvdvleuten/pim-mcp/commit/5dddcfcda320730deced5103b8ca976746ddaf38))
* add MCP server skeleton with serve and version commands ([8f22cff](https://github.com/robinvdvleuten/pim-mcp/commit/8f22cffb9faa048538a9028ee096736f72b66892))
* apply cancellations when importing ([1835294](https://github.com/robinvdvleuten/pim-mcp/commit/1835294a30fd12da19afd0e57c3e683b2524d791))
* apply EXDATE, RDATE and RECURRENCE-ID overrides ([331a201](https://github.com/robinvdvleuten/pim-mcp/commit/331a2018182bc6bfa23782b821904a49e4b7f1b1))
* discover servers from the email address ([a53d4f8](https://github.com/robinvdvleuten/pim-mcp/commit/a53d4f845b3ccd3278d4701c0f2009ab27f76be8))
* edit and delete single occurrences ([428dab8](https://github.com/robinvdvleuten/pim-mcp/commit/428dab8443cb3ee539e4a0cb263b6731cc5db2eb))
* expand recurring events into occurrences ([72e5e9b](https://github.com/robinvdvleuten/pim-mcp/commit/72e5e9b2c1a21cd7aed2d1697ac284d664247e58))
* harden the server and add accounts and recurring events ([a15b398](https://github.com/robinvdvleuten/pim-mcp/commit/a15b3987621d63cb2f7e0f18d02a5c946cc5dccb))
* import calendar invites from email ([7eaeea3](https://github.com/robinvdvleuten/pim-mcp/commit/7eaeea348c3524969613456d43bc390abc0c3f45))
* make the import preview a read-only tool ([299f703](https://github.com/robinvdvleuten/pim-mcp/commit/299f7034887da8dc135e773826cfcca16757183e))
* read mail over IMAP with go-imap v2 ([d04aeb4](https://github.com/robinvdvleuten/pim-mcp/commit/d04aeb4542275cb977347fc9a5a31fba330140ce))
* resolve time zones, all-day events and durations ([5b4e77d](https://github.com/robinvdvleuten/pim-mcp/commit/5b4e77d6d688bd4c720cf677cf911fbec15dc571))
* write calendar objects with ETag preconditions ([c93bb32](https://github.com/robinvdvleuten/pim-mcp/commit/c93bb32e3f64e895b827c756b8d17140048f04c5))


### Bug Fixes

* keep clock changes that Go hides on the last day of a leap year ([9f73374](https://github.com/robinvdvleuten/pim-mcp/commit/9f733747c47e5fae94615b3d503f5f85c2eb3100))
* order mail by date and give tool parameters one type ([32c1a40](https://github.com/robinvdvleuten/pim-mcp/commit/32c1a400d3e599cba274c17893d5d5a66adb1051))
* read the invites real mail actually contains ([b9571eb](https://github.com/robinvdvleuten/pim-mcp/commit/b9571eb3633a9ffeab636b2b715ebef19f4fb619))
