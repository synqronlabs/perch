# perch

Run your own IMAP and SMTP mail server on a homelab, NAS, Raspberry Pi, or spare PC with **virtually unlimited mailbox storage**, **no public IP**, **no port forwarding or tunnels**, and **none of the usual mail server complexity**.

[![asciicast](https://asciinema.org/a/crLSuCOxU10ANarU.svg)](https://asciinema.org/a/crLSuCOxU10ANarU)

`perch` is a tiny, single-binary mailbox server backed by [mxRaven](https://mxraven.com). Because mxRaven handles inbound MX, outbound delivery, SPF/DKIM/DMARC, IP reputation, and spam filtering at the edge, your hardware only needs outbound internet access and disk space (or an S3 bucket).

- **No public IP, port forwarding, or tunnels required.** Inbound mail is pulled over an outbound WebSocket stream from mxRaven, and outbound mail is relayed over standard authenticated submission. Your server can sit behind residential NAT or CGNAT.
- **Virtually unlimited mailbox size.** Store messages locally in SQLite (zstd-compressed and deduplicated) on cheap local drives, or offload raw `.eml` files to S3/R2 while keeping fast metadata and full-text search local.
- **Runs on old or minimal hardware.** Single static Go binary (CGO-free) with embedded SQLite and FTS5 full-text search.
- **Zero MTA maintenance.** No Postfix, Dovecot, Rspamd, DKIM signing daemons, or IP warm-up to manage. Moving messages to or from `Junk` automatically trains mxRaven's spam filter.
- **Run locally or remotely.** Bind to `127.0.0.1` or your LAN on a homelab, or expose it on a VPS with built-in Let's Encrypt (ACME) TLS.
- **Scriptable mail fetch and send.** `perch mail` is a POSIX-friendly IMAP and SMTP client that reads metadata as JSON, writes raw `.eml`, mbox, or Maildir, and sends MIME messages with attachments against the local server or any remote mail server.

## Install

Prebuilt binaries for Linux, macOS, and Windows are published on the
[releases page](https://github.com/synqronlabs/perch/releases). Every release
includes SHA-256 checksums, SBOMs, a keyless cosign signature, and GitHub build
provenance; see [RELEASING.md](RELEASING.md#verifying-a-release).

### With Go

If you have Go 1.26.4 or later, `go install` builds the latest tagged release
(CGO is not required):

```sh
go install github.com/synqronlabs/perch/cmd/perch@latest
```

The binary is written to `$(go env GOPATH)/bin` (usually `~/go/bin`); add that
directory to your `PATH` if it is not already there. To pin a specific version:

```sh
go install github.com/synqronlabs/perch/cmd/perch@v0.1.0
```

### From the latest GitHub release

This downloads the newest published release for your OS and architecture,
installs it to `~/.local/bin`, and works without a Go toolchain:

```sh
# Resolve the latest release tag (for example v0.1.0).
version=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
  https://github.com/synqronlabs/perch/releases/latest | sed 's#.*/tag/v##')
[ -n "$version" ] || { echo "could not determine the latest release" >&2; exit 1; }

# Pick the matching archive for this machine.
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  armv6l) arch=armv6 ;;
  armv7l) arch=armv7 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

archive="perch_${version}_${os}_${arch}.tar.gz"
curl -fsSLO "https://github.com/synqronlabs/perch/releases/download/v${version}/${archive}"
tar -xzf "${archive}"
mkdir -p ~/.local/bin
install -m 0755 "perch_${version}_${os}_${arch}/perch" ~/.local/bin/perch
```

Make sure `~/.local/bin` is on your `PATH`, then check the install:

```sh
perch version
```

To install system-wide instead, replace the last two lines with:

```sh
sudo install -m 0755 "perch_${version}_${os}_${arch}/perch" /usr/local/bin/perch
```

Windows users can download the `.zip` archive for their architecture from the
[releases page](https://github.com/synqronlabs/perch/releases).

### From source

Requires Go 1.26.4 or later (CGO-free build):

```sh
CGO_ENABLED=0 go build -trimpath -o perch ./cmd/perch
```

For a long-running Linux deployment, see [Run as a systemd
service](#run-as-a-systemd-service-linux), including optional timers for
backups and garbage collection.

## Quickstart (Homelab / Local Hardware)

### 1. Provision mxRaven and generate config

With an [mxRaven](https://mxraven.com) API token, `perch setup` automatically configures your domain, inbound route, submission listener, spam/auth rules, and writes `perch.yaml`:

```sh
export MXRAVEN_API_TOKEN=...

perch setup \
  --config ~/.local/share/perch/perch.yaml \
  --data-dir ~/.local/share/perch \
  --workspace my-workspace \
  --domain example.com \
  --hostname localhost
```

Publish the DNS records printed by the wizard (or pass `--wait` to wait until DNS verifies).

### 2. Create an account and start the server

```sh
perch account add --config ~/.local/share/perch/perch.yaml --email you@example.com
perch serve --config ~/.local/share/perch/perch.yaml
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

## Fetch and send mail from the command line

`perch mail` is a small IMAP and SMTP client for scripts, cron jobs, and
machines that do not run a full mail client. It works against the local
`perch serve` listeners or any remote mail server.

```sh
# Local server using the quickstart defaults.
perch mail list --server 127.0.0.1:1143 --plaintext --username you@example.com

# Remote server with implicit TLS (defaults to port 993).
perch mail list --server imaps://mail.example.com --username you@example.com

# STARTTLS (defaults to port 143).
perch mail search --server imap://mail.example.com --username you@example.com \
  --query 'from:alice since:2026-01-01'
```

Credentials can come from `--password-file`, `--password-stdin`,
`PERCH_IMAP_PASSWORD`, or an interactive prompt. `PERCH_IMAP_USER` supplies the
username. `--password` also works but is visible in process listings, so prefer
one of the other sources for automation. Add `--plaintext` for a loopback
listener; remote connections default to implicit TLS, and `imap://` requires
STARTTLS. Use `--ca-file` for a private CA and `--insecure` only for a
certificate you cannot verify. If `--server` is omitted, the address is taken
from `--config` (`imaps_addr` first, then `addr`).

Search expressions are joined with AND. Bare words search the message text;
supported keys are `from`, `to`, `cc`, `bcc`, `subject`, `body`, `text`,
`header:Name=Value`, `since`, `before`, `on`, `sent-since`, `sent-before`,
`sent-on`, `seen`, `unseen`, `answered`, `unanswered`, `flagged`, `unflagged`,
`deleted`, `undeleted`, `draft`, `undraft`, `larger`, `smaller`, `uid`,
`flag`/`keyword`, and `unflag`/`unkeyword`. Results are newest first by
default; `--reverse` flips the order. `--json` writes one JSON object per line.

```sh
# Message metadata as NDJSON.
perch mail search --server imaps://mail.example.com --username you@example.com \
  --query 'unseen larger:1M' --json

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

`fetch` uses `BODY.PEEK[]`, so it does not change `\Seen`; pass `--mark-seen`
to let the server mark messages read. Writing `eml` to stdout requires exactly
one message; use `mbox` or `--dest` for batches. `--dest` is a directory for
`eml` and `maildir` and a file for `mbox`. Existing files are not overwritten
unless `--overwrite` is passed.

### Sending mail

`perch mail send` composes a MIME message and submits it through the SMTP
submission listener.

```sh
# Simple text message.
perch mail send --from you@example.com --to alice@example.com \
  --subject 'Hello' --body 'Hi Alice'

# Read the body from stdin.
echo 'Hi Alice' | perch mail send --from you@example.com \
  --to alice@example.com --subject 'Hello'

# Attachments, Cc/Bcc, and both text and HTML bodies.
perch mail send \
  --from you@example.com \
  --to 'Alice <alice@example.com>' \
  --cc bob@example.net \
  --bcc audit@example.net \
  --subject 'Quarterly report' \
  --body-file message.txt \
  --html-file message.html \
  --attach report.pdf \
  --attach chart.png

# Compose and inspect the raw message without sending it.
perch mail send --from you@example.com --to alice@example.com \
  --subject 'Draft' --body 'Hello' --dry-run > message.eml
```

`--from` defaults to `--username` (or `PERCH_IMAP_USER`). When a password is
available, the SMTP username also defaults to the `From` address. SMTP
credentials and TLS options mirror the IMAP client with an `smtp-` prefix:
`--smtp-server`, `--smtp-plaintext`, `--smtp-starttls`,
`--smtp-implicit-tls`, `--smtp-ca-file`, and `--smtp-insecure`. If
`--smtp-server` is omitted, the submission listener in `--config` is used
(`tls_addr` first, then `addr`). For the quickstart plaintext listener, pass
`--smtp-plaintext` when you give an explicit `--smtp-server 127.0.0.1:1587`;
a config-derived `addr` selects plaintext automatically.

Bodies are quoted-printable unless `--8bitmime` is set, in which case they are
sent as UTF-8 with `BODY=8BITMIME`; attachments are always base64 encoded.
SMTPUTF8 is detected from non-ASCII envelope addresses. Force it with
`--smtputf8` or disable it with `--smtputf8=false`. `--verbose` prints the
server's EHLO capabilities and delivery details to stderr.

### SMTP extension support

`perch mail caps` prints the extensions advertised by the configured submission
server. Use `--smtp-server` to inspect another server:

```sh
perch mail caps
perch mail caps --smtp-server smtp.mxraven.email:587
```

The local perch submission relay advertises `PIPELINING`, `8BITMIME`,
`ENHANCEDSTATUSCODES`, `SIZE`, `SMTPUTF8`, `STARTTLS` when TLS is configured,
and `AUTH PLAIN LOGIN` when TLS or loopback insecure auth is enabled. It
forwards `SIZE`, `BODY=8BITMIME`, and `SMTPUTF8` upstream, but does not expose
`DSN`, `CHUNKING`, `BINARYMIME`, `REQUIRETLS`, or `DELIVERBY` because it always
relays the message body with `DATA`, not `BDAT`.

mxRaven's submission endpoint advertises `CHUNKING`, `SMTPUTF8`,
`REQUIRETLS`, `DELIVERBY`, and `BINARYMIME` in addition to the standard set,
but not `DSN`. Since perch's local relay does not pass those parameters
through, `perch mail send` only adds `--8bitmime` and `--smtputf8`; `SIZE` is
sent automatically. Run `perch mail caps --smtp-server smtp.mxraven.email:587`
to inspect the live upstream set.

## Archival with S3

If you want bottomless cloud storage, enable S3 mode. mxRaven writes incoming `.eml` files directly to your bucket via `S3_STORE`, and `perch` reconciles the bucket automatically:

```sh
export MXRAVEN_API_TOKEN=...
export PERCH_S3_SECRET_KEY=...

perch setup \
  --config ~/.local/share/perch/perch.yaml \
  --data-dir ~/.local/share/perch \
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

## Run as a systemd service (Linux)

`perch serve` runs in the foreground, logs to stderr, drains connections on
`SIGTERM`/`SIGINT` (waiting up to five seconds), and reloads certificate files
on `SIGHUP`, so it maps directly onto a `Type=simple` unit. It does not fork,
write a PID file, implement `sd_notify`, or support socket activation, so do
not use `Type=notify` or socket units.

The instructions below use a dedicated `perch` service account,
`/etc/perch/perch.yaml` for configuration, and `/var/lib/perch` for data.
Adjust the paths if your layout differs, and keep `data_dir` in the YAML in
sync with the unit's `StateDirectory` and `ReadWritePaths`.

> The built-in default config path comes from `os.UserConfigDir()`
> (`$XDG_CONFIG_HOME/perch/perch.yaml`, usually `~/.config/perch/perch.yaml`).
> Always pass `--config` explicitly in service units rather than relying on
> `$HOME`.

### 1. Create the service account and install

```sh
sudo useradd --system --home-dir /var/lib/perch --shell /usr/sbin/nologin perch
sudo install -Dm0755 perch /usr/local/bin/perch
sudo install -d -m 0700 -o perch -g perch /etc/perch /var/lib/perch

# Provision mxRaven and write the config as the service user.
sudo -u perch -H env MXRAVEN_API_TOKEN=... \
  /usr/local/bin/perch setup \
  --config /etc/perch/perch.yaml \
  --data-dir /var/lib/perch \
  --workspace my-workspace \
  --domain example.com \
  --hostname mail.example.com

# Create the first mailbox.
sudo -u perch -H /usr/local/bin/perch account add \
  --config /etc/perch/perch.yaml \
  --email you@example.com
```

Run `perch setup`, `perch init`, `perch account`, and `perch doctor` as the
service user (or `chown` their output afterwards). `perch` writes the config
with mode `0600` and the data directory with mode `0700`, so a root-owned
config will not be readable by the `perch` account.

Verify everything before starting the service:

```sh
sudo -u perch -H /usr/local/bin/perch doctor --config /etc/perch/perch.yaml
```

### 2. Create the unit

Write `/etc/systemd/system/perch.service`:

```ini
[Unit]
Description=perch mail server
Documentation=https://github.com/synqronlabs/perch
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=perch
Group=perch
ExecStart=/usr/local/bin/perch serve --config /etc/perch/perch.yaml
ExecReload=/bin/kill -HUP $MAINPID
EnvironmentFile=-/etc/perch/perch.env
Restart=on-failure
RestartSec=5s
TimeoutStopSec=15s
LimitNOFILE=65536
UMask=0077

# Must match data_dir in /etc/perch/perch.yaml.
StateDirectory=perch
StateDirectoryMode=0700
ReadWritePaths=/var/lib/perch

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

If you use `perch setup --public` (ports `143`/`993`/`587`/`465`) or ACME
(the HTTP-01 listener binds `:80`), add these two lines to the `[Service]`
section so the unprivileged service user can bind privileged ports:

```ini
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
```

Open the corresponding ports in your firewall; for a public deployment:

```sh
sudo ufw allow 80/tcp
sudo ufw allow 143,993,587,465/tcp
```

The default quickstart ports (`1143`/`1587`/`1993`/`1465`) are unprivileged and
need neither capabilities nor firewall changes for loopback use.

`ProtectSystem=strict` and `ProtectHome=true` assume the config lives under
`/etc` and the data directory under `/var/lib`. If you keep either under a home
directory, relax those options or add the necessary `ReadWritePaths`. The ACME
cache defaults to `<data_dir>/acme`, so it is writable via `StateDirectory`;
add it to `ReadWritePaths` if you set `tls.acme.cache_dir` elsewhere.

`ExecReload` sends SIGHUP, which `perch` only handles when `tls.cert_file` and
`tls.key_file` are configured. With ACME or a plaintext-only loopback setup,
remove the `ExecReload` line: SIGHUP would terminate the process.

### 3. Optional: secrets from the environment

`perch setup` writes listener and S3 secrets inline into the `0600` config by
default. To keep them out of the YAML, replace `key_secret:` with
`key_secret_env: PERCH_MXRAVEN_KEY_SECRET` (and similarly for the submission
listener) and `secret_key:` with `secret_key_env: PERCH_S3_SECRET_KEY`, then
put the values in `/etc/perch/perch.env`:

```sh
sudo install -m 0600 /dev/null /etc/perch/perch.env
sudo tee /etc/perch/perch.env >/dev/null <<'EOF'
PERCH_S3_SECRET_KEY=...
PERCH_MXRAVEN_KEY_SECRET=...
PERCH_MXRAVEN_SUBMISSION_KEY_SECRET=...
EOF
```

systemd reads `EnvironmentFile` as root before dropping privileges, so the file
can remain root-owned and unreadable by the service account.

### 4. Enable and verify

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now perch
sudo systemctl status perch
sudo journalctl -u perch -f
```

Logs are captured by journald; set `log.format: json` in `perch.yaml` for
structured output. Account, alias, and mailbox changes are written to SQLite
and apply immediately without a restart. After changing `perch.yaml`, run
`sudo systemctl restart perch`. When `tls.cert_file` and `tls.key_file` are
configured, manual certificates reload without dropping connections via
`sudo systemctl reload perch` (SIGHUP); ACME renews and reloads on its own and
needs no reload command.

### 5. systemd timers for maintenance

`perch gc` and `perch backup` are one-shot commands and are safe to run while
`perch serve` is running (SQLite WAL mode allows concurrent readers and
writers). `perch reconcile` does not need a timer: `serve` reconciles S3 at
startup and every `storage.s3.reconcile_every` (default `5m`).

#### Backup timer

`perch backup` refuses to overwrite an existing destination, so use a small
wrapper to add timestamps and prune old copies (adjust `-mtime +14` to match
your retention policy). Write `/usr/local/bin/perch-backup`:

```sh
sudo tee /usr/local/bin/perch-backup >/dev/null <<'EOF'
#!/bin/sh
set -eu
stamp=$(date +%Y%m%d-%H%M%S)
/usr/local/bin/perch backup --config /etc/perch/perch.yaml "/var/lib/perch/backups/perch-$stamp.db"
find /var/lib/perch/backups -type f -name 'perch-*.db' -mtime +14 -delete
EOF
sudo chmod 0755 /usr/local/bin/perch-backup
```

Write `/etc/systemd/system/perch-backup.service`:

```ini
[Unit]
Description=perch SQLite backup
After=perch.service

[Service]
Type=oneshot
User=perch
Group=perch
ExecStart=/usr/local/bin/perch-backup
EnvironmentFile=-/etc/perch/perch.env
UMask=0077
Nice=10
IOSchedulingClass=idle
```

Write `/etc/systemd/system/perch-backup.timer`:

```ini
[Unit]
Description=Daily perch backup

[Timer]
OnCalendar=*-*-* 03:30:00
RandomizedDelaySec=15m
Persistent=true
Unit=perch-backup.service

[Install]
WantedBy=timers.target
```

#### Garbage collection timer

`perch gc` removes orphaned local blobs, dead letters older than 30 days
(`--dead-letter-age`), and enforces the S3 spool limit, then runs an incremental
vacuum. Add `--s3-delete` if you also want it to delete S3 objects belonging to
deleted messages; without it, those tombstones accumulate.

Write `/etc/systemd/system/perch-gc.service`:

```ini
[Unit]
Description=perch garbage collection
After=perch.service

[Service]
Type=oneshot
User=perch
Group=perch
ExecStart=/usr/local/bin/perch gc --config /etc/perch/perch.yaml
EnvironmentFile=-/etc/perch/perch.env
UMask=0077
Nice=10
IOSchedulingClass=idle
```

Write `/etc/systemd/system/perch-gc.timer`:

```ini
[Unit]
Description=Daily perch garbage collection

[Timer]
OnCalendar=*-*-* 04:30:00
RandomizedDelaySec=15m
Persistent=true
Unit=perch-gc.service

[Install]
WantedBy=timers.target
```

#### Enable the timers

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now perch-backup.timer perch-gc.timer
systemctl list-timers --all 'perch-*'
```

Run a job immediately with `sudo systemctl start perch-backup.service` or
`sudo systemctl start perch-gc.service`; inspect results with
`journalctl -u perch-backup.service -u perch-gc.service`.

The timers run as `perch` and only need the same config and environment as the
main service. If `storage.mode` is `sqlite`, `--s3-delete` and the spool steps
are ignored. To take an on-demand backup, run the wrapper or
`sudo -u perch -H /usr/local/bin/perch backup --config /etc/perch/perch.yaml /path/to/copy.db`
(the destination must not already exist, and `perch` must be able to write to
its directory).

### 6. Optional: rootless user service

`perch` also works as a per-user service under `systemctl --user`. Put the unit
in `~/.config/systemd/user/`, drop `User=`, `Group=`, and `StateDirectory=`,
adjust the `ProtectHome`/`ProtectSystem`/`ReadWritePaths` options for your home
layout, and pass explicit `--config`/`--data-dir` paths. Enable lingering so the
service survives logout:

```sh
loginctl enable-linger "$USER"
systemctl --user enable --now perch
```

User services cannot acquire `CAP_NET_BIND_SERVICE`, so this only works with
unprivileged listener ports (the quickstart defaults, or custom ports above
`1024`). Use the system-level unit for `--public` or ACME.

## CLI Reference

```text
perch init        create a local configuration and optional first account
perch setup       provision mxRaven domain, listeners, keys, routes, and S3 rule
perch serve       run IMAP, submission, and the mxRaven notification consumer
perch account     add | list | passwd | disable | enable
perch alias       add | remove | list
perch mailbox     list
perch mail        list | search | fetch (IMAP client, local or remote)
perch mail        send | caps (SMTP submission client)
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
