---
title: Configuration
weight: 20
---

`perch init` and `perch setup` write YAML with `0600` permissions. The default
path is `$XDG_CONFIG_HOME/perch/perch.yaml` (usually
`~/.config/perch/perch.yaml`), but production deployments should pass
`--config` explicitly.

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
    secret_key: ""
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
environment variables. The mxRaven control-plane credential is used only by
`perch setup` and is never written to the config. Setup normally uses browser
approval; `MXRAVEN_API_TOKEN` selects the legacy token path.

## Environment overrides

| Variable | Purpose |
| --- | --- |
| `PERCH_CONFIG` | Default config path used by commands that do not pass `--config` |
| `PERCH_DATA_DIR` | Override `data_dir` |
| `PERCH_HOSTNAME` | Override `hostname` |
| `PERCH_IMAP_USER` | Default username for `perch mail` |
| `PERCH_IMAP_PASSWORD` | Password for `perch mail` |
| `PERCH_S3_SECRET_KEY` | Default S3 secret environment variable |
| `PERCH_MXRAVEN_KEY_SECRET` | mxRaven MTA listener secret |
| `PERCH_MXRAVEN_SUBMISSION_KEY_SECRET` | mxRaven submission listener secret |
| `MXRAVEN_API_TOKEN` | Legacy control-plane token for non-interactive setup |
| `MXRAVEN_ISSUER` | Override the mxRaven identity issuer URL |
| `MXRAVEN_DASHBOARD_URL` | Override the mxRaven dashboard approval URL |
| `MXRAVEN_CLIENT_ID` | Override the registered perch connected-app client ID |

## TLS

For a LAN-only deployment, `perch init --self-signed` generates a local
certificate. For a public VPS, `perch setup --public --acme-email ... --acme-domain ...`
enables Let's Encrypt and binds the standard ports.

`perch` requires TLS for non-loopback IMAP and submission connections. The
plaintext listeners are intended for `127.0.0.1` only.

See [Run as a systemd service](operations/systemd.md) for a
production example.
