---
title: Quickstart
weight: 20
---

perch has two setup paths. Try it locally without mxRaven, or connect an
mxRaven workspace to receive and send real mail.

## Try locally, no mxRaven needed

`perch init` creates a standalone configuration and first account without
contacting mxRaven.

```sh
perch init \
  --data-dir ~/.local/share/perch \
  --hostname localhost \
  --email you@example.com
```

It prompts for the account password. Start the server:

```sh
perch serve --config ~/.local/share/perch/perch.yaml
```

This gives you a local mailbox for testing IMAP clients and the read-only CLI
commands. It does not receive mail from the internet or relay outbound mail;
use the mxRaven path below for that.

Then [connect a mail client](#connect-a-mail-client).

## Receive real mail with mxRaven

To receive mail from the public internet and send outbound mail, perch uses an
[mxRaven](https://mxraven.com) workspace for the public edge: MX,
SPF/DKIM/DMARC, IP reputation, and spam filtering. The free plan includes one
workspace with 1,000 inbound and 1,000 outbound emails a month, with no card
required. See [mxRaven pricing](https://mxraven.com/pricing).

### 1. Provision mxRaven and generate config

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

### 2. Create an account and start the server

```sh
perch account add --config ~/.local/share/perch/perch.yaml --email you@example.com
perch serve --config ~/.local/share/perch/perch.yaml
```

## Connect a mail client

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

## Try the command-line client

```sh
# List mailboxes over the local plaintext listener.
perch mail list --server 127.0.0.1:1143 --plaintext --username you@example.com

# Search recent mail.
perch mail search --server 127.0.0.1:1143 --plaintext --username you@example.com \
  --query 'unseen' --json

# Send a message with an attachment (requires the mxRaven path above).
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
