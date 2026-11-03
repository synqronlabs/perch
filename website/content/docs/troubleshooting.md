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
