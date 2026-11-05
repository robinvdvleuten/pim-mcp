# Test calendar data

`ics/` holds iCalendar files and `golden/` the occurrences each one should expand to. The golden files are written by hand, not generated from the code, so a wrong result can't pass by matching itself.

Files named after a program (`outlook-*`, `teams-*`, `google-*`, `apple-*`, `zoom-*`, `nextcloud-*`, `thunderbird-*`) are **modelled** on how that program writes invites, not captured from it. They catch parser problems, but they can't show what a program really sends. Real samples should replace them, anonymized first. Never commit real mail or calendar data.
