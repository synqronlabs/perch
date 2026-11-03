---
title: Sending mail
weight: 20
---

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

## Bodies

The body is read from `--body`, `--body-file`, `--html-body`, `--html-file`, or
stdin when stdin is not a terminal.

- Text only → `text/plain`
- HTML only → `text/html`
- Both text and HTML → `multipart/alternative`
- Attachments → `multipart/mixed`

Attachments are base64 encoded and content types are detected from the file
extension or contents. Non-ASCII filenames use RFC 2231 encoding.

## Headers

- `--from` defaults to `--username` or `PERCH_IMAP_USER`.
- `--to`, `--cc`, `--bcc`, and `--reply-to` accept an address or a
  comma-separated list and can be repeated.
- `--bcc` recipients receive the message but do not appear in headers.
- `--header 'X-Name: value'` adds an extra header. Core headers are reserved.
- `--in-reply-to` and `--references` set threading headers.

## Credentials and transport

SMTP credentials and TLS options mirror the IMAP client with an `smtp-` prefix:

```text
--smtp-server  --smtp-plaintext  --smtp-starttls  --smtp-implicit-tls
--smtp-insecure  --smtp-ca-file
```

When `--smtp-server` is omitted, the submission listener in `--config` is used
(`tls_addr` first, then `addr`). For an explicit loopback address, pass
`--smtp-plaintext`.

When a password is available, the SMTP username defaults to the `From`
address. Use `--username` to authenticate as a different account.

## Transfer encodings

Bodies are quoted-printable by default. `--8bitmime` sends raw UTF-8 with
`BODY=8BITMIME`; attachments remain base64.

SMTPUTF8 is detected from non-ASCII envelope addresses and extra headers. Force
it with `--smtputf8`, or disable it with `--smtputf8=false`.

`--verbose` prints the server's EHLO capabilities and delivery details to
stderr.
