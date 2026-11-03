---
title: Fetching mail
weight: 10
---

`perch mail fetch` writes raw RFC 5322 messages to stdout, files, an mbox
stream, or a Maildir.

```sh
# One raw message on stdout.
perch mail fetch --server imaps://mail.example.com --username you@example.com \
  --uids 42 > message.eml

# An mbox stream for formail, procmail, or archival.
perch mail fetch --server imaps://mail.example.com --username you@example.com \
  --query 'from:alice' --format mbox > alice.mbox

# One .eml file per message.
perch mail fetch --server imaps://mail.example.com --username you@example.com \
  --query 'unseen' --dest ./messages

# Maildir, preserving flags.
perch mail fetch --server imaps://mail.example.com --username you@example.com \
  --query 'from:alice' --format maildir --dest ~/Mail
```

## Formats

| Format | Destination | Notes |
| --- | --- | --- |
| `eml` (default) | stdout for one message, or a directory | Writes `<uid>.eml` per message |
| `mbox` | stdout or a file | Escapes `From ` lines and separates messages |
| `maildir` | a Maildir root | Creates `tmp/`, `new/`, `cur/`; preserves flags |

`--dest` is a directory for `eml` and `maildir`, and a file for `mbox`. Existing
files are not overwritten unless `--overwrite` is passed.

## Selecting messages

Use `--uids`, a search expression, or `--all`:

```sh
perch mail fetch --uids 42
perch mail fetch --query 'unseen'
perch mail fetch --all --limit 100
```

`--limit` caps the number of messages. Fetch order is oldest first by default;
`--reverse` fetches newest first.

## Listing metadata

`perch mail search` prints a table by default and NDJSON with `--json`:

```sh
perch mail search --query 'from:alice' --json
```

Use `perch mail list` for mailboxes.
