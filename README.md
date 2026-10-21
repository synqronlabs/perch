# perch

A small, single-binary IMAP mailbox service backed by [mxRaven](https://mxraven.com).
SQLite holds metadata, flags, and the full-text index; message bodies live either
inside SQLite or in an S3 bucket written by mxRaven's `S3_STORE` action.

`perch` is designed for two deployments:

- **Local**: run it on a laptop or home server. IMAP and submission bind to
  loopback; no inbound ports or public DNS are required.
- **Remote**: run it on a VPS. IMAP and submission use TLS, and raw messages are
  archived in S3 by mxRaven while SQLite keeps only metadata and search text.

## What it does

- IMAP4rev1 server with IDLE, UIDPLUS, MOVE, NAMESPACE, SPECIAL-USE,
  LIST-EXTENDED, LIST-STATUS, STATUS=SIZE, ESEARCH, LITERAL+, SASL-IR, and
  APPENDLIMIT.
- Full-text search over subject, addresses, headers, body, and attachment names
  using SQLite FTS5.
- Pull-based inbound delivery through mxRaven's client notification stream. No
  webhook endpoint or public ingress is required.
- Streaming SMTP submission relay to mxRaven using the first-party
  [`raven`](https://github.com/synqronlabs/raven) server and client. Messages are
  relayed as the client sends them; perch does not spool outbound mail.
- Optional S3 archive mode using mxRaven `S3_STORE`, with bucket reconciliation
  to recover messages whose notification events were missed.
- Argon2id passwords, mailbox aliases, plus-addressing, catch-all domains, spam
  routing to `Junk`, and per-account IMAP namespaces.
- Built-in mxRaven setup wizard with optional DNS verification wait,
  authentication-aware MTA rules, Rspamd scanning, and per-listener rate limits.
- Spam/ham feedback: `$Junk` and `$NotJunk` keyword changes are submitted to
  mxRaven's feedback service using the exact stored `.eml` bytes.
- Bounded connection count, ingest concurrency, blob cache, and S3 spool.

## Build

Requires Go 1.26.4 or later.

```sh
go build -o perch ./cmd/perch
```

The build is CGO-free. `modernc.org/sqlite` includes FTS5.

## Local quickstart

```sh
./perch init \
  --data-dir ~/.local/share/perch \
  --hostname localhost \
  --email you@example.com \
  --name "You"

./perch serve --config ~/.local/share/perch/perch.yaml
```

`init` prompts for the account password if `--password` is omitted. Then point
Thunderbird or another IMAP client at:

| Setting | Value |
| --- | --- |
| IMAP server | `127.0.0.1` |
| IMAP port | `1143` |
| IMAP security | none (loopback only) |
| SMTP server | `127.0.0.1` |
| SMTP port | `1587` |
| SMTP security | none (loopback only) |
| Username | `you@example.com` |
| Password | the account password |

For a local encrypted setup, run `perch init --self-signed`. This generates a
certificate under `<data-dir>/tls/` and enables IMAPS on `127.0.0.1:1993` and
SMTPS on `127.0.0.1:1465`. Trust the generated certificate in the client first.

### Connecting mxRaven

You can create the mxRaven resources manually or use the wizard:

```sh
export MXRAVEN_API_TOKEN=...
./perch setup \
  --workspace acme-prod \
  --domain example.com \
  --hostname mail.example.com
```

The wizard creates or reuses:

- the sending domain,
- an MTA listener with terminal action `DROP`,
- an inbound route,
- client notifications and an MTA API key,
- a submission listener, sending-domain policy, and API key,
- optionally an S3 integration and `S3_STORE` routing rule.

It prints the DNS records you must publish and writes `perch.yaml`. Use
`--dry-run` to inspect the plan first.

Then create the first local account and start the service:

```sh
./perch account add --config <path>/perch.yaml --email you@example.com
./perch serve --config <path>/perch.yaml
```

## Remote deployment with TLS and S3

A typical remote invocation:

```sh
export PERCH_S3_SECRET_KEY=...
export MXRAVEN_API_TOKEN=...

./perch setup \
  --workspace acme-prod \
  --domain example.com \
  --hostname mail.example.com \
  --storage s3 \
  --s3-bucket my-mail-archive \
  --s3-region auto \
  --s3-endpoint https://<account>.r2.cloudflarestorage.com \
  --s3-access-key <read-only-key> \
  --public \
  --acme-email admin@example.com \
  --acme-domain mail.example.com \
  --wait
```

`--public` sets the standard ports:

| Service | Port |
| --- | --- |
| IMAP STARTTLS | `143` |
| IMAPS | `993` |
| SMTP submission STARTTLS | `587` |
| SMTPS | `465` |

You can use `--tls-cert` and `--tls-key` instead of ACME. ACME uses HTTP-01 on
port 80. `--wait` polls mxRaven until the sending domain and inbound route are
verified; use `--wait-timeout` and `--wait-interval` to tune it.

Authentication and inbound policy can be configured at setup time:

```sh
--auth-policy reject-dmarc-fail   # none | reject-dmarc-fail | reject-spf-fail | reject-unauthenticated
--rspamd                          # default true
--reject-malware                  # default true
--reject-high-confidence-spam     # optional
--mta-message-rate 60 --mta-recipient-rate 120 --mta-task-rate 60 --mta-burst 30 --mta-concurrency 10
```

The reject policies create mxRaven MTA rules using `authentication.*` and
`verdicts.*` expression facts. Perch still routes Rspamd spam verdicts to
`Junk`.

### S3 mode

In S3 mode, mxRaven writes the raw `.eml` with the `S3_STORE` action. The object
key template must be bucket-relative, identify the recipient with `{{.mailbox.*}}`,
include `{{.task.id}}`, and end in `.eml`; the wizard writes:

```text
perch/{{.mailbox.address}}/{{.task.id}}.eml
```

Perch needs `s3:GetObject`, `s3:HeadObject`, and `s3:ListBucket`. The mxRaven
storage integration needs write access. The setup wizard uses the same key for
both; for a manual deployment, give Perch a read-only key while mxRaven keeps the
write key.

On ingest, perch temporarily keeps a local copy so the message is immediately
readable, then evicts it after the S3 attempt confirms the object (unless
`storage.s3.spool` is enabled). A periodic reconciler lists the bucket and
indexes objects whose notification events were missed.

### Notification retention

mxRaven's client notification stream retains events for **24 hours**, and raw
message tokens live for about 25 hours. In SQLite-only mode, a perch instance
that is offline longer than that can miss mail. Run it as a service, and use S3
mode with reconciliation when long outages are possible.

## Configuration

`perch init` and `perch setup` write YAML with `0600` permissions. Important
sections:

```yaml
data_dir: /var/lib/perch
hostname: mail.example.com

imap:
  addr: 127.0.0.1:1143
  imaps_addr: ""
  allow_plaintext_loopback: true

submission:
  addr: 127.0.0.1:1587
  tls_addr: ""

tls:
  cert_file: ""
  key_file: ""
  acme:
    enabled: false
    email: ""
    domains: []
    http_addr: ":80"

storage:
  mode: sqlite            # sqlite | s3
  s3:
    endpoint: ""
    region: ""
    bucket: ""
    prefix: "perch/"
    access_key: ""
    secret_key_env: PERCH_S3_SECRET_KEY
    force_path_style: false
    key_template: "perch/{{.mailbox.address}}/{{.task.id}}.eml"
    reconcile_every: 5m
    spool: false
    spool_max_bytes: 268435456

limits:
  max_connections: 512
  ingest_concurrency: 2
  blob_cache_bytes: 67108864
  max_message_bytes: 26214400
  max_recipients: 100

mxraven:
  submission_host: smtp.mxraven.email
  submission_port: 587
  notification_url: wss://notifications.mxraven.com
  feedback_url: https://api.mxraven.com
  listeners:
    - id: ...
      kind: mta
      key_prefix: ...
      key_secret_env: PERCH_MXRAVEN_KEY_SECRET
      consumer: notify-...
    - id: ...
      kind: submission
      key_prefix: ...
      key_secret_env: PERCH_MXRAVEN_SUBMISSION_KEY_SECRET
```

Secrets can be inline (protected by file permissions) or referenced through
environment variables. The control-plane token is used only by `perch setup`
and is never written to the config.

## CLI

```text
perch init       create a local configuration and optional first account
perch setup      create mxRaven domains, listeners, keys, routes, and S3 rule
perch serve      run IMAP, submission, and the notification consumer
perch account    add | list | passwd | disable | enable
perch alias      add | remove | list
perch mailbox    list
perch doctor     check the database, FTS5, TLS, S3, and mxRaven connectivity
perch backup     write a consistent SQLite backup
perch version
```

`perch doctor` checks SQLite integrity, FTS5 availability, account/dead-letter
and outbound counts, configured limits, certificate expiry, S3 bucket/list
access and spool size, MX/DMARC DNS records, and mxRaven
notification/submission connectivity.

`perch gc` removes orphaned local blobs, enforces the S3 spool budget, and can
purge old dead letters. `--s3-delete` also removes S3 objects for messages the
user expunged. `perch reconcile` forces an immediate bucket scan.

## Storage and search

- SQLite runs in WAL mode with a single writer connection and a small reader
  pool.
- Local raw messages are zstd-compressed and content-addressed by SHA-256.
- Copies share the same blob; refcounts decide when local data is deleted.
- The FTS index is an external-content FTS5 table using the `unicode61`
  tokenizer. IMAP `TEXT`/`BODY` are substring semantics, so FTS is used as a
  candidate prefilter and the exact match is verified in Go.
- Spam and malware verdicts from mxRaven route messages to `Junk`.

## Security

- IMAP and submission authentication use Argon2id hashes.
- Plaintext authentication is allowed only on loopback.
- Off-loopback listeners refuse to start without TLS.
- Submission requires authentication and restricts `From`/`MAIL FROM` to the
  account's addresses and aliases.
- The IMAP server never opens inbound SMTP; mxRaven is the MX.
- Mailbox names are validated against path traversal and control characters.
- All SQL is parameterized; the FTS query is quoted.
- Raw-message access tokens and API secrets are never logged.

SQLite is not encrypted at rest. Use full-disk encryption for sensitive
deployments.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
```

The mxRaven SDK is pinned to `github.com/synqronlabs/mxraven-go v0.9.0`.
Raven is pinned to `github.com/synqronlabs/raven v1.10.0`.
