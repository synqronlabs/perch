# perch command reference for agents

Run `perch <command> --help` for the exact flags of the installed version. This
file records the parts that matter to an agent: selectors, output shapes,
credential handling, and side effects.

## Global behavior

- Default config: `$PERCH_CONFIG`, else `~/.config/perch/perch.yaml`.
  Always pass `--config` when it is known; agent harnesses often use a
  non-default path.
- Exit status: `0` success; non-zero failure. Usage errors are usually `1`;
  an unknown top-level command is `2`. Errors are printed to stderr as
  `perch: <message>`.
- `--json` exists only on `mail list` and `mail search`. It emits NDJSON (one
  JSON object per line), never a JSON array.
- Human-readable commands print tables to stdout; parse by header name, not
  fixed column offsets.
- `perch init`, `setup`, `serve`, `account`, `alias`, `mailbox`, `doctor`,
  `backup`, `reconcile`, `gc`, `outbound` read or write the local SQLite
  database directly. `perch mail` talks to an IMAP/SMTP server.

## Environment

| Variable | Purpose |
| --- | --- |
| `PERCH_CONFIG` | Default config path |
| `PERCH_DATA_DIR` | Override `data_dir` |
| `PERCH_HOSTNAME` | Override `hostname` |
| `PERCH_IMAP_USER` | Default mail username / sender |
| `PERCH_IMAP_PASSWORD` | Default mail password |
| `PERCH_S3_SECRET_KEY` | Default S3 secret env var used by the sample config |
| `PERCH_MXRAVEN_KEY_SECRET` | mxRaven MTA listener secret |
| `PERCH_MXRAVEN_SUBMISSION_KEY_SECRET` | mxRaven submission listener secret |
| `MXRAVEN_API_TOKEN` | Legacy control-plane token for `perch setup` |
| `MXRAVEN_ISSUER`, `MXRAVEN_DASHBOARD_URL`, `MXRAVEN_CLIENT_ID` | Setup overrides |

## Credentials, shared by `perch mail`

```text
--username, -u           account username (env PERCH_IMAP_USER)
--password               visible in process listings; avoid
--password-file FILE     read password from FILE
--password-stdin         read password from stdin
--timeout DURATION       connection timeout (default 30s)
--config FILE            config file
```

Use exactly one password source. If `--password-stdin` is used, the message
body must come from `--body*`/`--html*`, not stdin. If no source is available
and stdin is a terminal, perch prompts; in an agent harness that normally means
the command fails with `no password`.

## `perch mail list`

```sh
perch mail list --json
perch mail list --subscribed --json
perch mail list --server imaps://mail.example.com --username agent@example.com
```

Flags: `--json`, `--subscribed`, plus IMAP connection flags.

NDJSON fields:

```json
{"name":"INBOX","delimiter":"/","attributes":["\\HasNoChildren"],"special_use":["\\Inbox"],"subscribed":true}
```

## `perch mail search`

```sh
perch mail search -q 'unseen' --json
perch mail search -q 'from:alice since:2026-01-01' --limit 0
perch mail search --uids 12,14 --limit 0
perch mail search -q 'unseen' --mailbox Archive --reverse --json
```

Flags: `--mailbox, -m` (default `INBOX`), `--query, -q`, `--uids`, `--limit,
-n` (default 50; 0 unlimited), `--reverse`, `--json`, plus IMAP flags. A query
can also be passed positionally.

Ordering: newest UID first by default; `--reverse` is oldest first. The
mailbox is opened read-only.

Search grammar (terms are ANDed):

```text
bare words                 text anywhere (headers + body)
from: to: cc: bcc: subject: header:Name=Value
body: text:
since: before: on:          internal date, YYYY-MM-DD
sent-since: sent-before: sent-on:   Date header
seen unseen answered unanswered flagged unflagged deleted undeleted draft undraft
larger: smaller:            bytes; K/M/G suffix
uid:1,3:5,10:*              explicit UID set
flag:keyword unflag:keyword
```

Notes:

- Header searches are case-insensitive substring matches.
- `larger` means strictly greater; `smaller` strictly less.
- Short tokens (under 3 characters) are poor full-text candidates; combine
  them with a longer term when possible.
- Up to `limits.search_max_messages` messages are examined (default 50000).

NDJSON fields for each match:

```json
{"uid":42,"seq":7,"mailbox":"INBOX","subject":"Hi","from":[{"name":"Alice","address":"alice@example.com"}],"to":[{"address":"agent@example.com"}],"date":"2026-01-02T10:00:00Z","internal_date":"2026-01-02T10:00:01Z","size":2048,"flags":["\\Seen"],"message_id":"<abc@example.com>"}
```

`seq` is the IMAP sequence number in the selected mailbox. `message_id` is the
value to use for `--in-reply-to`/`--references`; perch prints it without angle
brackets and the CLI adds them if they are missing.

## `perch mail fetch`

```sh
perch mail fetch --uids 42
perch mail fetch -q 'from:alice' --format mbox > alice.mbox
perch mail fetch -q 'unseen' --format maildir --dest ~/Mail
perch mail fetch -q 'unseen' --dest ./messages --limit 100
perch mail fetch --all --dest ./all --overwrite
```

Flags: `--mailbox, -m`, `--query, -q`, `--uids`, `--all`, `--limit, -n`
(default 0 = unlimited), `--format, -f` (`eml`, `mbox`, `maildir`; default
`eml`), `--dest, -d, -o`, `--mark-seen`, `--overwrite`, `--reverse`, plus IMAP
flags.

Behavior:

- Uses `BODY.PEEK[]` by default; `--mark-seen` sets `\Seen`.
- `eml` to stdout requires exactly one message.
- `--dest` is a directory for `eml`/`maildir`, a file for `mbox`.
- `eml` writes `<uid>.eml`; `maildir` creates `tmp/new/cur` and preserves flags.
- Refuses to overwrite an existing `eml`/`mbox` without `--overwrite`.
- Refuses to run without `--uids`, a query, or `--all`.
- `fetch` default order is oldest first; `--reverse` is newest first. This is
  the opposite of `search`.

## `perch mail send`

```sh
perch mail send \
  --from agent@example.com \
  --to alice@example.com \
  --subject 'Hello' \
  --body 'Hi Alice'

printf 'Body from a file-like pipe\n' | perch mail send \
  --from agent@example.com --to alice@example.com --subject 'Hello'

perch mail send ... --dry-run > message.eml
```

Flags: `--from`, `--to`, `--cc`, `--bcc`, `--reply-to`, `--subject, -s`,
`--body`, `--body-file`, `--html-body`, `--html-file`, `--attach, -a`,
`--header, -H`, `--in-reply-to`, `--references`, `--8bitmime`, `--smtputf8`,
`--dry-run, -n`, `--verbose, -v`, plus SMTP flags.

SMTP flags: `--smtp-server, -S`, `--smtp-plaintext`, `--smtp-starttls`,
`--smtp-implicit-tls`, `--smtp-insecure`, `--smtp-ca-file`.

Behavior:

- `--from` defaults to `--username` or `PERCH_IMAP_USER`.
- At least one `--to`, `--cc`, or `--bcc` is required.
- `--to`/`--cc`/`--bcc` accept lists and are repeatable.
- Body from stdin is used only when no body flag is set and stdin is not a TTY.
- Text + HTML produces `multipart/alternative`; attachments wrap it in
  `multipart/mixed`.
- `--bcc` recipients are envelope-only.
- `--header` cannot override core headers (`From`, `To`, `Subject`, `Date`,
  `Message-ID`, `MIME-Version`, `Content-*`, `In-Reply-To`, `References`).
- `--references` is a space-separated list; values may include angle brackets
  but brackets are added automatically when missing.
- Success prints nothing. Use `--verbose` to see capabilities and the server
  message id on stderr, and `perch outbound list` for delivery status.
- A timeout does not prove failure. Check the outbound table and Sent mailbox
  before retrying.

## `perch mail caps`

```sh
perch mail caps --smtp-server smtp.mxraven.email:587
```

Performs EHLO and prints advertised extensions. No message and no credentials
are used. Useful for distinguishing a network/TLS problem from an auth
problem.

## Admin commands (local database)

```sh
perch account add --email agent@example.com --name 'Agent'
perch account list
perch account passwd --email agent@example.com
perch account disable --email agent@example.com
perch account enable --email agent@example.com
perch account delete --email agent@example.com --yes

perch alias add --account agent@example.com --address support@example.com
perch alias add --account agent@example.com --address example.com --catch-all
perch alias list --account agent@example.com
perch alias remove --address support@example.com

perch mailbox list --account agent@example.com
```

- `account add` and `account passwd` prompt when `--password` is omitted. There
  is no password-file/stdin variant; for unattended setup, pass `--password`
  only in a controlled environment and never log the command line.
- `account enable` prints `enabled <address>`; `account disable` prints
  `disabled <address>`.
- `account delete` requires `--yes` when stdin is not a terminal. It removes
  addresses, mailboxes, and messages immediately. S3 tombstones remain until
  `perch gc --s3-delete`.
- `account list` columns: `ID ADDRESS NAME DISABLED CREATED`.
- `mailbox list` columns: `NAME SPECIAL-USE SUBSCRIBED`. It reads SQLite
  directly and does not require serve.
- `alias list` prints one address per line.

## Diagnostics and maintenance

```sh
perch doctor --config /etc/perch/perch.yaml
perch backup --config /etc/perch/perch.yaml /var/lib/perch/backups/perch.db
perch reconcile --config /etc/perch/perch.yaml
perch gc --config /etc/perch/perch.yaml --dry-run
perch gc --config /etc/perch/perch.yaml
perch outbound list --limit 20
perch outbound list --account agent@example.com
```

- `doctor` checks SQLite integrity, FTS5, accounts/dead letters, outbound
  counts, limits, TLS expiry, DNS (MX/DMARC/DKIM CNAMEs), S3, mxRaven
  listeners, and submission. It exits non-zero on the first hard failure.
  Warnings are printed but do not fail the command. It can take ~20 seconds
  because of network checks.
- `backup` writes a consistent SQLite copy while serve is running. The
  destination must not exist.
- `reconcile` requires `storage.mode=s3`; it forces one bucket scan.
- `gc` removes orphan local blobs and old dead letters (default 30 days),
  enforces the S3 spool limit, and optionally deletes S3 tombstones. Use
  `--dry-run` first. `--s3-delete` is the destructive switch.
- `outbound list` columns: `CREATED STATUS FROM SUBJECT MESSAGE_REF ATTEMPT
  SMTP`. `CREATED` is RFC3339. `SMTP` is `code enhanced-status` when present.
  The table does not show `reason` or `remote_response`; query the database
  read-only for those (see `diagnostics.md`).

## Server commands

```sh
perch init --data-dir ~/.local/share/perch --hostname localhost \
  --email agent@example.com --password '...'
perch setup --config /etc/perch/perch.yaml --data-dir /var/lib/perch \
  --workspace my-workspace --domain example.com --hostname mail.example.com
perch serve --config /etc/perch/perch.yaml
```

- `init` creates a local config and SQLite database without contacting
  mxRaven. `--self-signed` adds a local certificate and TLS listeners.
- `setup` requires mxRaven approval (browser device code, `--token`, or
  `MXRAVEN_API_TOKEN`) and writes the config. It is interactive unless a token
  is supplied.
- `serve` runs in the foreground. SIGTERM/SIGINT drain connections; SIGHUP
  reloads manual TLS files when configured, and **terminates the process** when
  manual TLS files are not configured. Account/alias changes do not need a
  restart.

## Output and parsing notes

- NDJSON: decode each non-empty line separately. Do not `jq` the whole stream
  as one value unless you use `jq -s`.
- Tables are generated with `tabwriter`; column widths vary. Match header names
  or split with a regex if you must parse.
- Raw `fetch` output is bytes, not UTF-8 text; write it to a file and parse MIME.
- `perch` never emits JSON for `account`, `mailbox`, `outbound`, or `doctor`.
  Prefer commands plus header-aware parsing over scraping fixed positions.
