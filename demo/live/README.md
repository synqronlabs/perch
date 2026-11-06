# perch live demo

A shared, disposable perch instance for the Hacker News launch. Visitors click
once, get a random inbox on `tryperch.mxraven.com`, and can use the web terminal
or their own IMAP/SMTP client. No mxRaven signup.

## Current deployment

| Resource | Value |
| --- | --- |
| AWS account | `880405169318` (`mxraven-admin`) |
| Region / AZ | `eu-central-1` / `eu-central-1a` |
| Instance | `i-08fa25d44d111e6cc` (`t4g.small`, 24 GiB gp3) |
| Elastic IP | `18.156.206.179` (`eipalloc-05f67842dc99d3a6b`) |
| Security group | `sg-0a1082f3c0379bdd9` (`perch-demo`) |
| IAM role | `mxraven-perch-demo-ssm` (SSM only) |
| Artifacts bucket | `mxraven-perch-demo-artifacts-880405169318` |
| Public web | `https://tryperch.mxraven.com` |
| Public mail | `mail.tryperch.mxraven.com:993` (IMAPS), `:465` (SMTPS) |

The host runs:

- `perch-demo.service` — the shared perch server on loopback (`127.0.0.1:1143`, `127.0.0.1:1587`)
- `previewd.service` — session API, web UI, and WebSocket terminal
- `nginx` — HTTPS for the web app and TLS termination for IMAPS/SMTPS
- one disposable `perch-demo-terminal` container per browser session
- `perch-demo-proxy` — socat bridge from the internal Docker network to nginx

The terminal container has no internet route, only the internal Docker network
and the proxy. It drops all capabilities, runs as a non-root user, and has CPU,
memory, PID, and tmpfs limits.

## Status

Provisioned and running:

- `tryperch.mxraven.com` domain, inbound route, MTA listener, submission listener, and API keys
- submission listener default terminal action `REJECT`
- one allow rule: `len(envelope.rcpt_to) <= 5 && all(envelope.rcpt_to, # endsWith "@tryperch.mxraven.com")`
- Let's Encrypt certificate for `tryperch.mxraven.com` and `mail.tryperch.mxraven.com`
- session API, web UI, terminal containers, IMAPS/SMTPS frontend

The web app and IMAP login work. Outbound submission is currently rejected with
`MAIL FROM must use a verified domain granted to this submission listener`
because the mxRaven DNS records below are not published yet.

Remaining for launch:

1. Add the mxRaven DNS records: inbound TXT + MX, DKIM CNAMEs, bounces CNAME,
   and `_dmarc` TXT.
2. Open ports 443, 993, and 465 to the internet (they are currently restricted
   to the operator IP).
3. Send a real external message to a demo address and verify it arrives.
4. Verify an external recipient is rejected by the submission rule.

## DNS

The `mxraven.com` zone is Cloudflare zone `a37ce6783ac661f210dd45364045f382`.
Records must be **DNS-only** (not proxied) for HTTP-01 and direct IMAP/SMTP:

```text
tryperch.mxraven.com       A     18.156.206.179
mail.tryperch.mxraven.com  A     18.156.206.179
```

Plus the records mxRaven printed after `perch setup`:

```text
CNAME  bounces.tryperch.mxraven.com           vsdwgdfcjei2llrbxuqvublgbsy.bounces.dns.mxraven.email
CNAME  mxr1._domainkey.tryperch.mxraven.com   vsdwgdfcjei2llrbxuqvublgbsy.mxr1._domainkey.dkim.dns.mxraven.email
CNAME  mxr2._domainkey.tryperch.mxraven.com   vsdwgdfcjei2llrbxuqvublgbsy.mxr2._domainkey.dkim.dns.mxraven.email
TXT    _dmarc.tryperch.mxraven.com            "v=DMARC1; p=quarantine"
TXT    _mxraven-inbound.tryperch.mxraven.com  "mxraven-inbound=inb-vnjcfq2sqihrvaqexws6gc53hfe"
MX     tryperch.mxraven.com                   smtp.mxraven.email (priority 10)
```

## Operations

Run commands on the host through SSM:

```sh
aws --profile mxraven-admin ssm start-session --target i-08fa25d44d111e6cc
```

Or send a one-off command:

```sh
aws --profile mxraven-admin ssm send-command \
  --instance-ids i-08fa25d44d111e6cc \
  --document-name AWS-RunShellScript \
  --parameters 'commands=["systemctl status perch-demo previewd nginx --no-pager"]'
```

Useful checks:

```sh
curl -sS https://tryperch.mxraven.com/healthz
journalctl -u perch-demo -f
journalctl -u previewd -f
docker ps --format '{{.Names}} {{.Status}}'
docker logs perch-demo-proxy
```

## Deploying an update

Build ARM64 binaries locally:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
  -ldflags '-s -w -X github.com/synqronlabs/perch/internal/cli.Version=0.4.0' \
  -o /tmp/perch-demo-artifacts/perch ./cmd/perch

cd demo/live/previewd
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
  -ldflags '-s -w' -o /tmp/perch-demo-artifacts/previewd .
```

Upload to the artifacts bucket, fetch presigned URLs, and run
`deploy/install-host.sh` through SSM. The script is idempotent.

## Cutover to tryperch.mxraven.com

After mxRaven approval and DNS:

1. Upload the provisioned perch config and run `deploy/update-perch-config.sh`.
2. Update `ACCOUNT_DOMAIN=tryperch.mxraven.com` in
   `/etc/perch-demo/previewd.env` and restart `previewd`.
3. Run `deploy/issue-cert.sh` to replace the demo certificate with Let's
   Encrypt.
4. Create a fresh test session and send a message from an external mailbox to
   the demo address.

## Cost

Roughly:

- `t4g.small` on-demand: ~$12/month
- 24 GiB gp3: ~$2/month
- Elastic IP: ~$3.6/month
- S3 artifacts: cents
- Data transfer: negligible at demo volume

Stop or terminate the instance when the demo is finished. The Elastic IP is
released separately.
