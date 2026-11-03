---
title: Backup and garbage collection
weight: 20
---

`perch gc` and `perch backup` are one-shot commands and are safe to run while
`perch serve` is running: SQLite WAL mode allows concurrent readers and
writers.

`perch reconcile` does not need a timer. `serve` reconciles S3 at startup and
every `storage.s3.reconcile_every` (default `5m`).

## Backup timer

`perch backup` refuses to overwrite an existing destination. Use a wrapper to
add timestamps and prune old copies. Write `/usr/local/bin/perch-backup`:

```sh
sudo tee /usr/local/bin/perch-backup >/dev/null <<'EOF'
#!/bin/sh
set -eu
stamp=$(date +%Y%m%d-%H%M%S)
/usr/local/bin/perch backup --config /etc/perch/perch.yaml "/var/lib/perch/backups/perch-$stamp.db"
find /var/lib/perch/backups -type f -name 'perch-*.db' -mtime +14 -delete
EOF
sudo chmod 0755 /usr/local/bin/perch-backup
```

`/etc/systemd/system/perch-backup.service`:

```ini
[Unit]
Description=perch SQLite backup
After=perch.service

[Service]
Type=oneshot
User=perch
Group=perch
ExecStart=/usr/local/bin/perch-backup
EnvironmentFile=-/etc/perch/perch.env
UMask=0077
Nice=10
IOSchedulingClass=idle
```

`/etc/systemd/system/perch-backup.timer`:

```ini
[Unit]
Description=Daily perch backup

[Timer]
OnCalendar=*-*-* 03:30:00
RandomizedDelaySec=15m
Persistent=true
Unit=perch-backup.service

[Install]
WantedBy=timers.target
```

## Garbage collection timer

`perch gc` removes orphaned local blobs, dead letters older than 30 days
(`--dead-letter-age`), and enforces the S3 spool limit, then runs an incremental
vacuum. Add `--s3-delete` to also delete S3 objects belonging to deleted
messages; without it, those tombstones accumulate.

`/etc/systemd/system/perch-gc.service`:

```ini
[Unit]
Description=perch garbage collection
After=perch.service

[Service]
Type=oneshot
User=perch
Group=perch
ExecStart=/usr/local/bin/perch gc --config /etc/perch/perch.yaml
EnvironmentFile=-/etc/perch/perch.env
UMask=0077
Nice=10
IOSchedulingClass=idle
```

`/etc/systemd/system/perch-gc.timer`:

```ini
[Unit]
Description=Daily perch garbage collection

[Timer]
OnCalendar=*-*-* 04:30:00
RandomizedDelaySec=15m
Persistent=true
Unit=perch-gc.service

[Install]
WantedBy=timers.target
```

Enable both timers:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now perch-backup.timer perch-gc.timer
systemctl list-timers --all 'perch-*'
```

Run a job immediately with `sudo systemctl start perch-backup.service` or
`sudo systemctl start perch-gc.service`; inspect results with
`journalctl -u perch-backup.service -u perch-gc.service`.

The timers run as `perch` and only need the same config and environment as the
main service. In SQLite mode, `--s3-delete` and the spool steps are ignored.
