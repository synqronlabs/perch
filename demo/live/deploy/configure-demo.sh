#!/bin/bash
# Update the public demo identity and restart previewd.
set -euo pipefail
DEMO_HOST="${DEMO_HOST:-tryperch.mxraven.com}"
MAIL_HOST="${MAIL_HOST:-mail.tryperch.mxraven.com}"
ACCOUNT_DOMAIN="${ACCOUNT_DOMAIN:-tryperch.mxraven.com}"
TURNSTILE_SITE_KEY="${TURNSTILE_SITE_KEY:-}"
TURNSTILE_SECRET_KEY="${TURNSTILE_SECRET_KEY:-}"
cat > /etc/perch-demo/previewd.env <<ENV
PREVIEWD_FLAGS=-listen 127.0.0.1:8080 -perch-config /etc/perch-demo/perch.yaml -perch-bin /usr/local/bin/perch -domain $ACCOUNT_DOMAIN -mail-host $MAIL_HOST -imap-port 993 -smtp-port 465 -ttl 15m -max-sessions 50 -terminal-image perch-demo-terminal:latest -docker-network perch-demo-internal -proxy-ip 172.31.240.2 -state-dir /var/lib/perch-demo -docker-bin /usr/bin/docker -secure-cookie=true
TURNSTILE_SITE_KEY=$TURNSTILE_SITE_KEY
TURNSTILE_SECRET_KEY=$TURNSTILE_SECRET_KEY
ENV
chmod 0600 /etc/perch-demo/previewd.env
systemctl restart previewd.service
systemctl --no-pager --lines=20 status previewd.service
