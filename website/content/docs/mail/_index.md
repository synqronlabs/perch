---
title: Mail client
weight: 50
bookCollapseSection: true
---

`perch mail` is a small IMAP and SMTP client for scripts, cron jobs, and
machines that do not run a full mail client. It works against the local
`perch serve` listeners or any remote mail server.

The command reference is generated from the installed command tree:

- [`perch mail list`](../cli/perch-mail-list.md)
- [`perch mail search`](../cli/perch-mail-search.md)
- [`perch mail fetch`](../cli/perch-mail-fetch.md)
- [`perch mail send`](../cli/perch-mail-send.md)
- [`perch mail caps`](../cli/perch-mail-caps.md)

`perch mail list` lists mailboxes (folders), not messages. Use
`perch mail search` to list or search messages.

For agent and application patterns, see
[Agents and applications](agents.md). If you are wiring a coding agent into a
mailbox, the repository also ships an Agent Skill that installs with
`npx skills add https://github.com/synqronlabs/perch --skill perch`. It covers
inbox polling, replies with attachments, credential-safe CLI use, and the
read-only diagnostic path.

## Connection flags

IMAP commands accept `--server`, `--plaintext`, `--starttls`, `--insecure`,
`--ca-file`, and the shared credential flags. SMTP commands use the same
credentials with `--smtp-server`, `--smtp-plaintext`, `--smtp-starttls`,
`--smtp-implicit-tls`, `--smtp-insecure`, and `--smtp-ca-file`.

Credentials can come from `--password-file`, `--password-stdin`,
`PERCH_IMAP_PASSWORD`, or an interactive prompt. `PERCH_IMAP_USER` supplies the
username. `--password` also works but is visible in process listings, so prefer
one of the other sources for automation.

If `--server`/`--smtp-server` is omitted, the matching listener is taken from
`--config`. For the quickstart plaintext listener, pass `--plaintext` or
`--smtp-plaintext` when giving an explicit loopback address. Config-derived
loopback listeners select plaintext automatically.

## Search expressions

Search expressions are joined with AND. Bare words search the message text.
Supported keys:

```text
from to cc bcc subject body text header:Name=Value
since before on sent-since sent-before sent-on
seen unseen answered unanswered flagged unflagged deleted undeleted draft undraft
larger smaller uid flag/keyword unflag/unkeyword
```

Examples:

```sh
perch mail search -q 'from:alice since:2026-01-01'
perch mail search -q 'unseen larger:1M' --json
perch mail search --uids 12,14 --limit 0
```

Results are newest first by default; `--reverse` flips the order. `--json`
writes one JSON object per line (NDJSON).

## Fetching

`fetch` uses `BODY.PEEK[]`, so it does not change `\Seen`; pass `--mark-seen`
to let the server mark messages read. Writing `eml` to stdout requires exactly
one message; use `mbox` or `--dest` for batches.

See [Fetching mail](fetch.md) for formats and examples.

## Sending

`perch mail send` composes MIME messages with text, HTML, and attachments and
submits them over SMTP. See [Sending mail](send.md).
