# perch

Run your own IMAP and SMTP mail server on a homelab, NAS, Raspberry Pi, or spare PC with **virtually unlimited mailbox storage**, **no public IP**, **no port forwarding or tunnels**, and **none of the usual mail server complexity**.

`perch` is a tiny, single-binary mailbox server backed by [mxRaven](https://mxraven.com). Because mxRaven handles inbound MX, outbound delivery, SPF/DKIM/DMARC, IP reputation, and spam filtering at the edge, your hardware only needs outbound internet access and disk space (or an S3 bucket).

- **No public IP, port forwarding, or tunnels required.** Inbound mail is pulled over an outbound WebSocket stream from mxRaven, and outbound mail is relayed over standard authenticated submission. Your server can sit behind residential NAT or CGNAT.
- **Virtually unlimited mailbox size.** Store messages locally in SQLite (zstd-compressed and deduplicated) on cheap local drives, or offload raw `.eml` files to S3/R2 while keeping fast metadata and full-text search local.
- **Runs on old or minimal hardware.** Single static Go binary (CGO-free) with embedded SQLite and FTS5 full-text search.
- **Zero MTA maintenance.** No Postfix, Dovecot, Rspamd, DKIM signing daemons, or IP warm-up to manage. Moving messages to or from `Junk` automatically trains mxRaven's spam filter.
- **Run locally or remotely.** Bind to `127.0.0.1` or your LAN on a homelab, or expose it on a VPS with built-in Let's Encrypt (ACME) TLS.

## Install

Requires Go 1.26.4 or later (CGO-free build):

```sh
go build -o perch ./cmd/perch
```

## Quickstart (Homelab / Local Hardware)

### 1. Provision mxRaven and generate config

With an [mxRaven](https://mxraven.com) API token, `perch setup` automatically configures your domain, inbound route, submission listener, spam/auth rules, and writes `perch.yaml`:

```sh
export MXRAVEN_API_TOKEN=...

./perch setup \
  --workspace my-workspace \
  --domain example.com \
  --hostname localhost
```

Publish the DNS records printed by the wizard (or pass `--wait` to wait until DNS verifies).

### 2. Create an account and start the server

```sh
./perch account add --config ~/.local/share/perch/perch.yaml --email you@example.com
./perch serve --config ~/.local/share/perch/perch.yaml
```

> Want to test locally before connecting mxRaven? Run `./perch init --data-dir ~/.local/share/perch --hostname localhost --email you@example.com` instead.

### 3. Connect your mail client

Point Thunderbird, Apple Mail, or any IMAP client to:

| Setting | Value |
| --- | --- |
| IMAP | `127.0.0.1:1143` (plaintext on loopback) |
| SMTP | `127.0.0.1:1587` (plaintext on loopback) |
| Username | `you@example.com` |
| Password | your account password |

To connect from other devices on your LAN, generate a self-signed certificate with `perch init --self-signed` (IMAPS on `1993`, SMTPS on `1465`) or provide your own TLS certificate via `--tls-cert` and `--tls-key`. Off-loopback connections always require TLS.

## Archival with S3

If you want bottomless cloud storage, enable S3 mode. mxRaven writes incoming `.eml` files directly to your bucket via `S3_STORE`, and `perch` reconciles the bucket automatically:

```sh
export MXRAVEN_API_TOKEN=...
export PERCH_S3_SECRET_KEY=...

./perch setup \
  --workspace my-workspace \
  --domain example.com \
  --hostname mail.example.com \
  --storage s3 \
  --s3-bucket my-mail-archive \
  --s3-region auto \
  --s3-endpoint https://<account>.r2.cloudflarestorage.com \
  --s3-access-key <access-key>
```

To expose `perch` directly on a public VPS with standard ports (`143`/`993`/`587`/`465`) and automatic Let's Encrypt certificates, add:

```sh
  --public \
  --acme-email admin@example.com \
  --acme-domain mail.example.com
```

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

## CLI Reference

```text
perch init        create a local configuration and optional first account
perch setup       provision mxRaven domain, listeners, keys, routes, and S3 rule
perch serve       run IMAP, submission, and the mxRaven notification consumer
perch account     add | list | passwd | disable | enable
perch alias       add | remove | list
perch mailbox     list
perch doctor      verify SQLite, FTS5, TLS, DNS, S3, and mxRaven connectivity
perch reconcile   force an immediate S3 bucket scan
perch gc          clean up orphaned local blobs and enforce S3 spool limits
perch backup      write a consistent SQLite backup
perch version     print version
```

## Features at a Glance

- **Standard IMAP4rev1**: `IDLE`, `UIDPLUS`, `MOVE`, `NAMESPACE`, `SPECIAL-USE`, `LIST-EXTENDED`, `LIST-STATUS`, `STATUS=SIZE`, `ESEARCH`, `LITERAL+`, `SASL-IR`, `APPENDLIMIT`.
- **Full-text search**: SQLite FTS5 across subject, addresses, headers, body, and attachment names.
- **Aliases & catch-all**: Per-account aliases, `user+tag@domain` plus-addressing, and catch-all domains.
- **Inbound policy**: Configurable SPF/DMARC rejection, malware rejection, Rspamd routing to `Junk`, and listener rate limits via `perch setup` flags.
- **Secure**: Argon2id password hashes, TLS required off-loopback, authenticated sender enforcement, and zero inbound SMTP ports.
- **Private**: mxRaven never persists your emails. All your emails remain with you on your S3 bucket or local disk.

## License

Released under the **MIT No Attribution (MIT-0)** license. See [`LICENSE`](LICENSE).
