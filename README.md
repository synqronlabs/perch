<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="logo-white.svg">
    <img src="logo.svg" width="70" height="55" alt="perch by mxRaven">
  </picture>
</p>

# perch by mxRaven

perch is a tiny single-binary IMAP + SMTP server with a POSIX CLI. Run it on a
homelab, give an AI agent an inbox, or wire mail into your app without a
public IP, port forwarding.

- **1 file, zero sidecars.** A CGO-free Go binary with embedded SQLite and FTS5. No Postfix, Dovecot, Rspamd, or container orchestra.
- **No public IP required.** Inbound mail arrives over an outbound WebSocket. Works behind NAT, CGNAT, or a homelab router.
- **Your storage, your rules.** Keep compressed mail in SQLite or offload raw `.eml` files to S3/R2 while metadata and search stay local.
- **Agent-ready CLI.** POSIX commands with NDJSON, raw `.eml`, mbox, Maildir, and real exit codes. Easy to wire into an agent loop.
- **Standard protocols.** IMAP and authenticated SMTP. Use Thunderbird, Apple Mail, mutt, or your own application client.
- **Private by default.** Self-hosted mail with zero inbound SMTP ports. [mxRaven](https://mxraven.com) does not retain your messages.

[mxRaven](https://mxraven.com) handles the public edge: MX, SPF/DKIM/DMARC, IP reputation, and spam
filtering so perch can stay inside your network. Your mail stays on your disk
or bucket.

## Use it as

- **A personal mailbox.** Point any standard mail client at it and keep the data on your hardware.
- **An agent inbox.** Give an AI agent an address it can search, read, reply to, and attach files to.
- **An application mail layer.** Provision inboxes for users or services, read via IMAP, and send via SMTP or the CLI.

## For agents and harnesses

```sh
# Structured context
perch mail search -q 'unseen from:alice' --json

# Full message for RAG or archival
perch mail fetch --uids 42 > message.eml

# Reply with an attachment
perch mail send --to alice@example.com \
  --subject 'Report ready' \
  --body 'See attached.' \
  --attach report.pdf
```

No browser sessions, no scraping, and no vendor-specific mailbox API. The same
commands work against a local perch server or any remote IMAP/SMTP server. See
[Agents and applications](https://perch.mxraven.com/docs/mail/agents/)
for more patterns.

### Install the agent skill

perch ships an Agent Skill that teaches coding agents to run inbox workflows
and diagnose connection, auth, and delivery failures with the CLI.

```sh
npx skills add https://github.com/synqronlabs/perch --skill perch
```

Add `-g` to make it available across projects, or target a harness with
`-a pi` / `-a hermes-agent`. Browse it first with `--list`.

## Documentation

Full documentation, including configuration, S3 storage, TLS, systemd, mail-client recipes, and the generated CLI reference:

**https://perch.mxraven.com/**

- [Installation](https://perch.mxraven.com/docs/getting-started/installation/)
- [Authorize perch with mxRaven](https://perch.mxraven.com/docs/getting-started/authorize-perch/)
- [Quickstart](https://perch.mxraven.com/docs/getting-started/quickstart/)
- [Configuration](https://perch.mxraven.com/docs/configuration/)
- [Mail client](https://perch.mxraven.com/docs/mail/)
- [Agents and applications](https://perch.mxraven.com/docs/mail/agents/)
- [Sending mail](https://perch.mxraven.com/docs/mail/send/)
- [CLI reference](https://perch.mxraven.com/docs/cli/)
- [Troubleshooting](https://perch.mxraven.com/docs/troubleshooting/)

The documentation sources live in [`website/content/docs`](website/content/docs)
and are published by the `Website` workflow.

## Install

Prebuilt binaries for Linux, macOS, and Windows are published on the
[releases page](https://github.com/synqronlabs/perch/releases). Every release
includes SHA-256 checksums, SBOMs, keyless cosign signatures, GitHub build
provenance, and manpages.

With Go 1.26.4 or later:

```sh
go install github.com/synqronlabs/perch/cmd/perch@latest
```

Or build from source:

```sh
CGO_ENABLED=0 go build -trimpath -o perch ./cmd/perch
```

See [Installation](https://perch.mxraven.com/docs/getting-started/installation/)
for the release download script, Windows notes, and manpage installation.

## Quickstart

```sh
# 1. Provision mxRaven and write perch.yaml. perch prints a code and opens the
# dashboard for a workspace owner to approve. See:
# https://perch.mxraven.com/docs/getting-started/authorize-perch/
perch setup \
  --config ~/.local/share/perch/perch.yaml \
  --data-dir ~/.local/share/perch \
  --workspace my-workspace \
  --domain example.com \
  --hostname localhost

# 2. Create an account and start the server.
perch account add --config ~/.local/share/perch/perch.yaml --email you@example.com
perch serve --config ~/.local/share/perch/perch.yaml
```

Point Thunderbird, Apple Mail, or any IMAP client at the loopback listeners:

| Setting | Value |
| --- | --- |
| IMAP | `127.0.0.1:1143` (plaintext on loopback) |
| SMTP | `127.0.0.1:1587` (plaintext on loopback) |
| Username | `you@example.com` |
| Password | your account password |

`perch mail` works against the same listeners:

```sh
perch mail list --server 127.0.0.1:1143 --plaintext --username you@example.com
perch mail send --smtp-server 127.0.0.1:1587 --smtp-plaintext \
  --from you@example.com --to alice@example.com \
  --subject 'Hello' --body 'See attached.' --attach report.pdf
```

See the [Quickstart](https://perch.mxraven.com/docs/getting-started/quickstart/)
for LAN TLS, real mail clients, and the first `perch mail` recipes.

## Manpages

Release archives include `man/*.1` pages for `perch`, its subcommands, and the
mail client. Install them system-wide with:

```sh
sudo install -d /usr/local/share/man/man1
sudo install -m 0644 man/perch*.1 /usr/local/share/man/man1/
sudo mandb
```

Then use `man perch`, `man perch-mail`, or `man perch-mail-send`. Manpages are
generated from the same command tree as `--help` and the website CLI reference.

## Development

```sh
make test          # go test ./...
make race          # go test -race ./...
make vet lint
make docs          # regenerate manpages and website CLI reference
make website       # build the Hugo documentation site
make snapshot      # cross-build release archives locally
```

Generated docs are checked in CI with `make docs-check`; run `make docs` after
changing commands or flags.

## License

Released under the **MIT No Attribution (MIT-0)** license. See [`LICENSE`](LICENSE).
