# Diagnosing perch

Work from the least invasive evidence to the most invasive. Do not change a
production deployment to "see what happens"; collect evidence first.

## 1. Classify the symptom

Ask which layer is failing:

- **Client can't connect** — wrong address, serve down, firewall, TLS mismatch.
- **Login fails** — password, disabled account, throttle, wrong credential type.
- **Inbound mail missing** — mxRaven delivery, account/alias resolution, Junk,
  S3 reconcile, dead letters.
- **Outbound mail missing/failed** — submission listener, sender authorization,
  upstream mxRaven, retry/deferral, outbound table.
- **Search/read is wrong** — wrong mailbox, flags, FTS index, message parsing.
- **Service won't stay up** — config/permissions/TLS/systemd sandbox.

## 2. Collect a snapshot

From the skill directory:

```sh
sh scripts/perch-diag.sh --config /etc/perch/perch.yaml
```

The collector is read-only. It prints the version, config path and
permissions, `doctor`, accounts, outbound rows, process/listener hints, and
recent logs where available. It never prints the config body or secrets.

Manually, the minimum useful set is:

```sh
perch version
perch doctor --config "$cfg"
perch account list --config "$cfg"
perch outbound list --config "$cfg" --limit 20
systemctl status perch --no-pager
journalctl -u perch -n 100 --no-pager
```

Run `doctor` as the same user that runs `serve`; file-permission problems are
invisible to root.

## 3. Read doctor output correctly

`doctor` runs checks in order and returns non-zero on the first **hard**
failure. Warnings are printed but do not fail the command. A partial report
means the failure is at the last printed check.

| Output | Meaning |
| --- | --- |
| `warning: configuration file is readable by group or others` | tighten to `0600`; secrets may be inline |
| `database: ok` | SQLite open + integrity check passed |
| `FTS5: ok (N indexed messages)` | FTS5 is present; `N` is indexed rows, not total messages |
| `accounts: N; dead letters: M` | `M > 0` means inbound messages had no matching account/alias |
| `outbound: accepted=…, delivered=…` | counts by status |
| `TLS: not configured (loopback plaintext only)` | non-loopback clients must use TLS or a proxy |
| `TLS certificate expires: …` | renew before the warning window (30 days) |
| `warning: no MX records…` / no DMARC / no CNAME | DNS not published; normal for local tests, a real problem in production |
| `S3: ok (bucket)` | endpoint/credentials/bucket worked |
| `S3 objects under "prefix": N` | listing works; compare with expected mail volume |
| `S3 spool: N objects, B bytes (limit L)` | local cache pressure |
| `mxRaven … listener …: ok` | notification stream credentials accepted |
| `mxRaven submission: ok` | STARTTLS + auth to the submission host worked |

## 4. Config and startup failures

`perch` validates the config before serving. Exact validation errors worth
recognizing:

| Error | Fix |
| --- | --- |
| `data_dir is required` | set `data_dir` |
| `hostname is required` | set `hostname` |
| `storage.s3.bucket is required in s3 mode` | set bucket |
| `storage.s3.key_template is required in s3 mode` | set template |
| `storage.s3.key_template must include {{.task.id}}, end with .eml, and identify a mailbox address` | template must contain `{{.task.id}}`, end in `.eml`, and include `{{.mailbox.address}}` or both local-part/domain placeholders |
| `storage.s3.key_template must start with storage.s3.prefix` | make the template begin with the exact prefix |
| `storage.s3.secret_key or secret_key_env is required in s3 mode` | set a secret or env var name |
| `tls.cert_file and tls.key_file must be set together` | supply both or neither |
| `tls.acme.domains is required when ACME is enabled` | add domains |
| `mxraven.listeners[N] requires key_prefix and a secret` | fill in listener credentials |
| `mxraven.listeners[N].kind must be mta or submission` | correct the kind |
| `at least one listener address is required` | set an IMAP or submission address |
| `limits.max_message_bytes must be positive` | fix the limit |

`data_dir` is not resolved relative to the config file. A relative `data_dir`
is resolved against the **current working directory** of each `perch` process.
If you run `doctor` or `account list` from a different directory than
`perch serve`, you may be reading a different (often newly created, empty)
database. Run from the serve directory or use an absolute `data_dir`.

Startup/listener errors:

- `IMAP listener <addr> requires TLS or a loopback bind` — bind loopback or
  configure `tls.cert_file`/`tls.key_file`/ACME.
- `IMAPS listener <addr> requires tls.cert_file and tls.key_file` — add TLS
  material or remove `imaps_addr`.
- `listen on IMAP address <addr>: address already in use` — another process is
  bound; find it with `ss -ltnp` or change the port.
- `submission listener is not configured` — the deployment is inbound-only or
  the config lost its submission listener.
- `validate config "...": ...` — the config is loaded before anything starts;
  fix the named field.

## 5. IMAP / login failures

Evidence:

```sh
perch mail list --config "$cfg" --json
perch account list --config "$cfg"
perch mailbox list --config "$cfg" --account agent@example.com
journalctl -u perch -n 100 --no-pager | grep -Ei 'imap|login|throttle'
```

Log lines:

- `IMAP login failed` with `username`, `remote`, and `error` — bad password or
  unknown/disabled account. When the account is disabled, the error is often
  `store: account is disabled`.
- `IMAP login throttled` — `authlimit` blocked the remote/username pair after
  repeated failures. Wait for the window or fix the credential; do not hammer.
- `IMAP login failed` with no error — generic verification failure; check the
  account is enabled and the password is the account password, not the API key.

Checklist:

- The account is present and `DISABLED` is `false`.
- The username is the full address (primary address or alias).
- The password source is correct and does not include a trailing newline from a
  file (`--password-file` trims CR/LF).
- For an explicit loopback address, `--plaintext` was passed when the listener
  is plaintext.
- For non-loopback, the certificate is trusted by the client; use `--ca-file`
  or install the CA.
- The mailbox name exists in `perch mail list --json` (e.g. `Junk`, `Archive`).

## 6. Inbound mail missing

Inbound is delivered by mxRaven over an outbound WebSocket; there is no public
SMTP port. A message can be accepted by mxRaven but never appear locally.

Evidence:

```sh
perch doctor --config "$cfg"
perch account list --config "$cfg"
perch alias list --account agent@example.com --config "$cfg"
journalctl -u perch -n 200 --no-pager | grep -Ei 'inbound|matching|dead|s3|reconcile|notification'
```

Log lines and causes:

- `no mxRaven listeners configured; inbound and submission events will not be consumed`
  — add `mxraven.listeners` with the `mta`/`submission` keys.
- `notification stream error` / `submission notification consumer stopped` —
  WebSocket credentials, network, or mxRaven outage.
- `inbound message has no matching account` with `task` and `recipients` — the
  recipient has no primary address, alias, catch-all, or plus-address match.
  This also happens when the only matching account is **disabled**. Add the
  account/alias or enable it. The raw message is stored as a dead letter.
- `accepted event has no recipients` — malformed accepted event; inspect the
  mxRaven task.
- `cannot parse mailbox from S3 key` / `S3 object has no matching account` —
  S3 key template and mailbox address disagree; fix the template and
  reconcile.
- `mxRaven S3 archive attempt failed` — S3 credentials/endpoint/bucket; the
  local copy may still exist if the spool is enabled.
- Spam/malware verdicts route to `Junk`, not `INBOX`. Search both.

Dead letters are counted by `doctor` but not exposed by a dedicated command.
To inspect reasons read-only:

```sh
sqlite3 -readonly "$cfg_dir/perch.db" \
  "SELECT datetime(created_at,'unixepoch') AS at, address, task_id, reason
   FROM dead_letter ORDER BY id DESC LIMIT 20;"
```

Do not delete a dead letter until you know why it was created. `perch gc`
removes dead letters older than `--dead-letter-age` (30 days by default).

## 7. Outbound / submission failures

Evidence:

```sh
perch mail caps --config "$cfg"
perch outbound list --config "$cfg" --limit 20
journalctl -u perch -n 200 --no-pager | grep -Ei 'submission|outbound|relay|mxraven'
```

Client-side errors:

- `submission listener is not configured` — add a submission listener or pass
  `--smtp-server`.
- `cannot connect to mxRaven submission: …` — network, TLS, or listener key
  problem. `doctor` tests the same path.
- `upstream submission failed` / `upstream command failed` — mxRaven rejected
  or broke the SMTP conversation; the real response is in the SMTP error and
  may be in `outbound` details.
- `sender not authorized` / `553` — `From` must be the account's primary
  address, an alias, or an authorized plus-address. Check
  `perch alias list --account …`.
- `null sender is not allowed on submission` — do not send bounces through the
  authenticated submission listener.
- `message exceeds append limit` — message/attachment is over
  `limits.max_message_bytes` (default 25 MiB). Do not raise the limit casually.

Outbound status meanings:

| Status | Meaning |
| --- | --- |
| `accepted` | mxRaven accepted the message; delivery attempts may follow |
| `delivered` | destination accepted it |
| `deferred` | temporary failure; retry is expected (`next_retry_at`) |
| `failed` | permanent failure; inspect SMTP code/enhanced status/reason |
| `expired` | delivery deadline passed |
| `suppressed` | mxRaven suppressed delivery (policy/suppression list) |

The `outbound list` table omits `reason` and `remote_response`. Read them
read-only:

```sh
sqlite3 -readonly "$cfg_dir/perch.db" \
  "SELECT datetime(created_at,'unixepoch') AS at, status, from_address, subject,
          attempt, smtp_code, enhanced_status, remote_response, reason
   FROM outbound ORDER BY id DESC LIMIT 20;"
```

If a send times out, do not immediately resend. Check the outbound row and the
Sent mailbox; the upstream may already have accepted it.

## 8. S3 mode

Only relevant when `storage.mode: s3`.

Evidence:

```sh
perch doctor --config "$cfg"
perch reconcile --config "$cfg"
perch gc --config "$cfg" --dry-run
```

Check:

- Endpoint, region, bucket, access key, and secret.
- `secret_key_env` names an environment variable that is actually present in
  the service environment (`EnvironmentFile` for systemd).
- `force_path_style` matches the provider (needed by some MinIO/R2 setups).
- `key_template` starts with `storage.s3.prefix`, contains `{{.task.id}}`, ends
  in `.eml`, and identifies the mailbox address.
- mxRaven's `S3_STORE` rule writes to the same bucket/prefix.
- `serve` reconciles at startup and every `reconcile_every` (default `5m`).
- `spool` keeps a bounded local copy; `spool_max_bytes` is enforced by `gc`.

`perch reconcile` in SQLite mode fails with
`reconcile requires storage.mode=s3`.

## 9. TLS and certificates

- Manual cert/key must be set together.
- Non-loopback IMAP/submission requires TLS; plaintext is loopback-only.
- Self-signed certificates require `--ca-file` (preferred) or `--insecure`
  (test only).
- `doctor` prints the expiry and warns within 30 days.
- SIGHUP reloads manual TLS files; with no manual TLS files it terminates
  serve, so do not send SIGHUP in that configuration.
- ACME requires `tls.acme.domains` and an HTTP listener reachable on port 80.

## 10. systemd / service exits

```sh
systemctl status perch --no-pager
journalctl -u perch -n 200 --no-pager
systemctl cat perch
```

- Use `Type=simple`; perch does not fork, write a PID file, or support socket
  activation.
- `ProtectHome`/`ProtectSystem` must allow `data_dir` and the config/cert
  paths.
- The service user must read the config and write the data directory.
- `ExecReload` (SIGHUP) is only safe with manual `tls.cert_file`/`tls.key_file`.
- Check the config file mode: `0600` recommended; group/other-readable inline
  secrets are a finding.

## 11. Database checks (read-only, last resort)

Prefer `perch doctor`, `perch account list`, `perch mailbox list`, and
`perch outbound list`. If you need rows the CLI does not show, use SQLite in
read-only mode and never write while `serve` is running. Do not use SQL
`UPDATE`/`INSERT`/`DELETE` to work around a problem, even on a copy; use the
supported `perch` command so validation, audit, and follow-up behavior match
what the service expects.

```sh
db="$cfg_dir/perch.db"
sqlite3 -readonly "$db" "PRAGMA integrity_check;"
sqlite3 -readonly "$db" "SELECT COUNT(*) FROM messages;"
sqlite3 -readonly "$db" "SELECT COUNT(*) FROM search_index;"
sqlite3 -readonly "$db" \
  "SELECT m.uid, m.subject, datetime(m.internal_date,'unixepoch')
   FROM messages m JOIN mailboxes b ON b.id=m.mailbox_id
   JOIN accounts a ON a.id=m.account_id
   WHERE a.primary_address='agent@example.com' AND b.name='INBOX'
   ORDER BY m.uid DESC LIMIT 10;"
```

If `sqlite3` is unavailable, use Python's stdlib `sqlite3` with
`file:...?mode=ro`. Preserve a copy before any repair. `perch backup` refuses
to overwrite, so use a fresh timestamped path.

## 12. Report template

When you finish diagnosing, report:

```text
Symptom:
Environment: perch <version>, config <path>, storage mode, local/remote
Evidence: exact commands and key output lines (redact secrets)
Root cause:
Fix: safest first step, then the durable fix
Verification: the command/output that proves it works
Risk: anything destructive, needs restart, or needs user approval
```

Diagnosis is read-only. Do not clone the database or start a second server to
test the proposed fix unless the user asks; report the verification command and
let them approve the mutation. Keep the investigation to roughly 5-10 commands.
If you are still unsure after that, report the evidence, the leading hypothesis,
and the specific unknown rather than expanding into source-code archaeology.
