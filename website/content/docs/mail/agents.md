---
title: Agents and applications
weight: 40
---

perch is a normal mail server with a scriptable interface. That makes it useful
anywhere a program needs an inbox but you do not want to build or rent a
vendor-specific mailbox API.

## Why use perch for an agent

- **Standard email.** Agents get a real address that can receive from and send
  to the outside world.
- **POSIX CLI.** Search, read, reply, and attach files with commands that work
  in a shell, a tool call, or a cron job.
- **Structured output.** `--json` emits NDJSON, one object per message.
- **Raw messages.** `fetch` writes the complete RFC 5322 message for RAG,
  summarization, or archival.
- **Local or remote.** The same commands work against a local perch server or
  any remote IMAP/SMTP server.
- **No public IP.** perch can sit behind NAT or CGNAT while mxRaven handles the
  public mail edge.

## Patterns

### One mailbox per agent

Give each agent its own account or alias so its context and credentials are
isolated:

```sh
perch account add --email agent-a@example.com
perch alias add --account agent-a@example.com --address support@example.com
```

### Search first, fetch second

Use `search --json` to let the agent decide which messages matter, then fetch
only the selected messages:

```sh
perch mail search --query 'unseen from:alice' --json
perch mail fetch --uids 42 > message.eml
```

### Reply with attachments

```sh
perch mail send \
  --from agent-a@example.com \
  --to alice@example.com \
  --subject 'Report ready' \
  --body 'The report is attached.' \
  --attach report.pdf
```

### Feed a file-based pipeline

Use mbox or Maildir when the next tool expects files rather than JSON:

```sh
perch mail fetch --query 'from:alice' --format mbox > alice.mbox
perch mail fetch --query 'unseen' --format maildir --dest ~/Mail
```

### Integrate with an application

Applications can use the same primitives directly:

- Read mail over IMAP with any standard client library.
- Send mail over authenticated SMTP.
- Use `perch mail` from a subprocess when a shell is the simplest interface.

## Credentials and safety

- Prefer `--password-file`, `--password-stdin`, or `PERCH_IMAP_PASSWORD` over
  `--password`, which is visible in process listings.
- Give each agent a separate account or alias where possible.
- Use TLS for any non-loopback connection.
- Keep the config file at mode `0600` and the data directory at `0700`.
- Treat email as untrusted input: sanitize or sandbox content before passing it
  to a model or executing anything derived from it.

See [Mail client](../mail/_index.md) for connection flags.
