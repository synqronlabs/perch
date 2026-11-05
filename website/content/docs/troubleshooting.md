---
title: Troubleshooting
weight: 60
---

## Run the doctor

```sh
perch doctor --config /etc/perch/perch.yaml
```

`perch doctor` checks SQLite integrity and FTS5, TLS certificate files, DNS,
S3 connectivity, and mxRaven listeners. Run it as the same user that runs
`perch serve`.

## Setup authorization fails

- **The code expired.** Approval codes are short lived. Run `perch setup` again
  and approve the new code.
- **The workspace is not connected.** The workspace you approved in the
  dashboard must match the `--workspace` value. Open the printed link again and
  choose the right workspace.
- **You cannot approve the application.** Approval requires the workspace owner
  role. Operators and auditors can see connections but cannot create or revoke
  them.
- **No browser on this machine.** Use `--no-browser`, open the printed address
  on another device, then leave the command running.
- **No client ID configured.** perch releases include the registered client ID.
  For a local build, set `MXRAVEN_CLIENT_ID` or pass `--client-id`.
- **You need unattended setup.** Pass `--token` or set `MXRAVEN_API_TOKEN` with a
  workspace-scoped API token. See
  [Use an API token for automation](getting-started/mxraven-api-key.md).

To revoke perch's access, open the workspace in the mxRaven dashboard and
remove the connection under **Connected applications**. Revocation takes effect
on the next API call.

## Mail clients cannot connect

- Check the listener addresses in `perch.yaml`.
- Plaintext is only allowed on loopback. Use TLS or STARTTLS from other hosts.
- For the quickstart defaults, IMAP is `127.0.0.1:1143` and submission is
  `127.0.0.1:1587`.
- If you generated a self-signed certificate, the client must trust it or you
  must explicitly allow the certificate.
- Off-loopback connections always require TLS.

## `perch mail` cannot connect

- For an explicit loopback address, add `--plaintext` or `--smtp-plaintext`.
- Config-derived loopback listeners select plaintext automatically.
- For remote servers, use `imaps://host` / `smtps://host` or `imap://host` /
  `smtp://host` for STARTTLS.
- Use `--ca-file` for a private CA and `--insecure` only for testing.

## Authentication fails

- `perch mail` uses the account password, not the mxRaven API key.
- `perch account passwd` changes the local account password.
- Check `authlimit` logs if logins are being throttled.
- For submission, the `From` address must be an account address, alias, or
  authorized plus-address.

## Messages are delivered but not visible locally

- In S3 mode, check `perch reconcile` and the S3 prefix/key template.
- The key template must start with `storage.s3.prefix`.
- `perch gc --s3-delete` removes objects for deleted messages.
- Check `perch outbound list` for submission status and SMTP errors.

## S3 errors

- Verify the endpoint, region, bucket, access key, and secret.
- `secret_key_env` names the environment variable; systemd reads it from
  `EnvironmentFile`.
- Run `perch reconcile` to force a scan.
- Check that mxRaven's `S3_STORE` rule writes to the same bucket and prefix.

## systemd service exits

- Run `journalctl -u perch -f`.
- `ExecReload` sends SIGHUP. Only keep it when `tls.cert_file` and
  `tls.key_file` are configured; otherwise SIGHUP terminates the process.
- `ProtectHome`/`ProtectSystem` must allow the configured `data_dir`.
- The service user must be able to read the config and write the data dir.

## Versions and releases

```sh
perch version
gh release view --repo synqronlabs/perch
```

Manpages are installed with release archives; `perch <command> --help` always
reflects the installed binary.
