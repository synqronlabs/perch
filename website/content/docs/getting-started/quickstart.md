---
title: Quickstart
weight: 20
---

This quickstart provisions mxRaven, creates a local account, starts perch, and
connects a mail client.

## 1. Provision mxRaven and generate config

Run `perch setup` with your workspace slug and domain. perch prints a code and
opens the mxRaven dashboard so a workspace owner can approve the application.
See [Authorize perch with mxRaven](authorize-perch.md) for the approval steps.

```sh
perch setup \
  --config ~/.local/share/perch/perch.yaml \
  --data-dir ~/.local/share/perch \
  --workspace my-workspace \
  --domain example.com \
  --hostname localhost
```

`perch setup` configures your domain, inbound route, submission listener,
spam/auth rules, and writes `perch.yaml`. Publish the DNS records printed by the
wizard, or pass `--wait` to wait until DNS verifies.

For CI or a terminal without a browser, pass `--token` or set
`MXRAVEN_API_TOKEN` instead. See
[Use an API token for automation](mxraven-api-key.md).

## 2. Create an account and start the server

```sh
perch account add --config ~/.local/share/perch/perch.yaml --email you@example.com
perch serve --config ~/.local/share/perch/perch.yaml
```

To test locally before connecting mxRaven:

```sh
./perch init --data-dir ~/.local/share/perch --hostname localhost --email you@example.com
```

## 3. Connect a mail client

Point Thunderbird, Apple Mail, mutt, or any IMAP client at:

| Setting | Value |
| --- | --- |
| IMAP | `127.0.0.1:1143` (plaintext on loopback) |
| SMTP | `127.0.0.1:1587` (plaintext on loopback) |
| Username | `you@example.com` |
| Password | your account password |

To connect from other devices on the LAN, generate a self-signed certificate
with `perch init --self-signed` (IMAPS on `1993`, SMTPS on `1465`) or provide
your own certificate via `--tls-cert` and `--tls-key`. Off-loopback connections
always require TLS.

## 4. Try the command-line client

```sh
# List mailboxes over the local plaintext listener.
perch mail list --server 127.0.0.1:1143 --plaintext --username you@example.com

# Search recent mail.
perch mail search --server 127.0.0.1:1143 --plaintext --username you@example.com \
  --query 'unseen' --json

# Send a message with an attachment.
perch mail send --smtp-server 127.0.0.1:1587 --smtp-plaintext \
  --from you@example.com --to alice@example.com \
  --subject 'Hello' --body 'See attached.' --attach report.pdf
```

When `--server` or `--smtp-server` is omitted, the matching listener is taken
from `--config`.

## Next steps

- [Configuration](../configuration.md)
- [Mail client](../mail/_index.md)
- [Run as a systemd service](../operations/systemd.md)
- [S3 archival](../storage/s3.md)
