---
title: systemd service
weight: 10
---

`perch serve` runs in the foreground, logs to stderr, drains connections on
`SIGTERM`/`SIGINT` (waiting up to five seconds), and reloads certificate files
on `SIGHUP`, so it maps directly onto a `Type=simple` unit. It does not fork,
write a PID file, implement `sd_notify`, or support socket activation; do not
use `Type=notify` or socket units.

The examples below use a dedicated `perch` service account,
`/etc/perch/perch.yaml`, and `/var/lib/perch`. Adjust paths if your layout
differs, and keep `data_dir` in YAML in sync with `StateDirectory` and
`ReadWritePaths`.

## 1. Create the account and install

```sh
sudo useradd --system --home-dir /var/lib/perch --shell /usr/sbin/nologin perch
sudo install -Dm0755 perch /usr/local/bin/perch
sudo install -d -m 0700 -o perch -g perch /etc/perch /var/lib/perch

# Provision mxRaven and write the config as the service user. The device code
# is printed; approve it from a browser, or pass --token for unattended setup.
sudo -u perch -H /usr/local/bin/perch setup \
  --config /etc/perch/perch.yaml \
  --data-dir /var/lib/perch \
  --workspace my-workspace \
  --domain example.com \
  --hostname mail.example.com \
  --no-browser

# Create the first mailbox.
sudo -u perch -H /usr/local/bin/perch account add \
  --config /etc/perch/perch.yaml \
  --email you@example.com
```

Run `perch setup`, `perch init`, `perch account`, and `perch doctor` as the
service user (or `chown` their output afterwards). perch writes the config with
mode `0600` and the data directory with mode `0700`, so a root-owned config
will not be readable by the `perch` account.

Verify everything before starting the service:

```sh
sudo -u perch -H /usr/local/bin/perch doctor --config /etc/perch/perch.yaml
```

## 2. Create the unit

Write `/etc/systemd/system/perch.service`:

```ini
[Unit]
Description=perch mail server
Documentation=https://github.com/synqronlabs/perch
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=perch
Group=perch
ExecStart=/usr/local/bin/perch serve --config /etc/perch/perch.yaml
ExecReload=/bin/kill -HUP $MAINPID
EnvironmentFile=-/etc/perch/perch.env
Restart=on-failure
RestartSec=5s
TimeoutStopSec=15s
LimitNOFILE=65536
UMask=0077

# Must match data_dir in /etc/perch/perch.yaml.
StateDirectory=perch
StateDirectoryMode=0700
ReadWritePaths=/var/lib/perch

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

If you use `perch setup --public` (ports `143`/`993`/`587`/`465`) or ACME, add:

```ini
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
```

Open the corresponding ports in your firewall. For a public deployment:

```sh
sudo ufw allow 80/tcp
sudo ufw allow 143,993,587,465/tcp
```

The default quickstart ports (`1143`/`1587`/`1993`/`1465`) are unprivileged and
need neither capabilities nor firewall changes for loopback use.

`ExecReload` sends SIGHUP, which perch only handles when `tls.cert_file` and
`tls.key_file` are configured. With ACME or a plaintext-only loopback setup,
remove the `ExecReload` line: SIGHUP would terminate the process.

## 3. Optional: secrets from the environment

`perch setup` writes listener and S3 secrets inline into the `0600` config by
default. To keep them out of YAML, replace `key_secret:` with
`key_secret_env: PERCH_MXRAVEN_KEY_SECRET` (and similarly for the submission
listener) and `secret_key:` with `secret_key_env: PERCH_S3_SECRET_KEY`, then
put the values in `/etc/perch/perch.env`:

```sh
sudo install -m 0600 /dev/null /etc/perch/perch.env
sudo tee /etc/perch/perch.env >/dev/null <<'EOF'
PERCH_S3_SECRET_KEY=...
PERCH_MXRAVEN_KEY_SECRET=...
PERCH_MXRAVEN_SUBMISSION_KEY_SECRET=...
EOF
```

systemd reads `EnvironmentFile` as root before dropping privileges, so the file
can remain root-owned and unreadable by the service account.

## 4. Enable and verify

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now perch
sudo systemctl status perch
sudo journalctl -u perch -f
```

Logs are captured by journald; set `log.format: json` in `perch.yaml` for
structured output. Account, alias, and mailbox changes apply immediately
without a restart. After changing `perch.yaml`, run
`sudo systemctl restart perch`.

## Rootless user service

perch also works as a per-user service under `systemctl --user`. Put the unit
in `~/.config/systemd/user/`, drop `User=`, `Group=`, and `StateDirectory=`,
adjust `ProtectHome`/`ProtectSystem`/`ReadWritePaths`, and pass explicit
`--config`/`--data-dir` paths. Enable lingering:

```sh
loginctl enable-linger "$USER"
systemctl --user enable --now perch
```

User services cannot acquire `CAP_NET_BIND_SERVICE`, so this only works with
unprivileged listener ports.

See [Backup and garbage collection](maintenance.md)
for timer examples.
