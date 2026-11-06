#!/bin/bash
# Issue and install a Let's Encrypt certificate using the nginx webroot.
# Run only after tryperch.mxraven.com and mail.tryperch.mxraven.com resolve to
# this host and port 80 is reachable from the internet.
set -euo pipefail
export HOME=/root
DEMO_HOST="${DEMO_HOST:-tryperch.mxraven.com}"
MAIL_HOST="${MAIL_HOST:-mail.tryperch.mxraven.com}"
EMAIL="${ACME_EMAIL:-admin@mxraven.com}"
ACME=/root/.acme.sh/acme.sh

if [ ! -x "$ACME" ]; then
  dnf install -y cronie >/dev/null
  systemctl enable --now crond >/dev/null 2>&1 || true
  curl -fsSL https://get.acme.sh | sh -s "email=$EMAIL"
fi

"$ACME" --set-default-ca --server letsencrypt
"$ACME" --issue --server letsencrypt \
  -d "$DEMO_HOST" -d "$MAIL_HOST" \
  -w /var/www/acme --keylength ec-256

"$ACME" --install-cert -d "$DEMO_HOST" --ecc \
  --key-file /etc/perch-demo/tls/privkey.pem \
  --fullchain-file /etc/perch-demo/tls/fullchain.pem \
  --reloadcmd "systemctl reload nginx"

chmod 0640 /etc/perch-demo/tls/privkey.pem /etc/perch-demo/tls/fullchain.pem
chgrp nginx /etc/perch-demo/tls/privkey.pem /etc/perch-demo/tls/fullchain.pem
systemctl reload nginx
systemctl restart perch-demo.service
