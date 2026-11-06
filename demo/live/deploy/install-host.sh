#!/bin/bash
# Installs the perch live demo on an Amazon Linux 2023 host.
# Artifacts are expected in /opt/perch-demo/artifacts:
#   perch, previewd, terminal.tar.gz, perch.yaml
set -euo pipefail

DEMO_HOST="${DEMO_HOST:-tryperch.mxraven.com}"
MAIL_HOST="${MAIL_HOST:-mail.tryperch.mxraven.com}"
ACCOUNT_DOMAIN="${ACCOUNT_DOMAIN:-tryperch.mxraven.com}"
PUBLIC_IP="${PUBLIC_IP:-}"
ARTIFACT_DIR="${ARTIFACT_DIR:-/opt/perch-demo/artifacts}"
INSTALL_DIR=/opt/perch-demo
DATA_DIR=/var/lib/perch-demo
CONFIG_DIR=/etc/perch-demo
TLS_DIR=$CONFIG_DIR/tls

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }

log "installing packages"
dnf install -y docker nginx nginx-mod-stream jq tar gzip unzip openssl >/dev/null
systemctl enable --now docker

if ! id perchdemo >/dev/null 2>&1; then
  useradd --system --home-dir "$DATA_DIR" --shell /sbin/nologin perchdemo
fi
usermod -aG docker perchdemo

install -d -m 0755 "$INSTALL_DIR" "$CONFIG_DIR" "$TLS_DIR" /var/www/acme
install -d -m 0700 -o perchdemo -g perchdemo "$DATA_DIR" "$DATA_DIR/sessions"

log "installing binaries and configuration"
install -m 0755 "$ARTIFACT_DIR/perch" /usr/local/bin/perch
install -m 0755 "$ARTIFACT_DIR/previewd" /usr/local/bin/previewd
if [ -f "$ARTIFACT_DIR/perch.yaml" ]; then
  install -m 0600 -o perchdemo -g perchdemo "$ARTIFACT_DIR/perch.yaml" "$CONFIG_DIR/perch.yaml"
fi

if [ ! -f "$TLS_DIR/fullchain.pem" ]; then
  log "generating self-signed demo CA and server certificate"
  openssl req -x509 -newkey rsa:2048 -nodes -days 90 \
    -keyout "$TLS_DIR/demo-ca.key" -out "$TLS_DIR/demo-ca.pem" \
    -subj "/CN=perch live demo CA" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes \
    -keyout "$TLS_DIR/privkey.pem" -out "$TLS_DIR/server.csr" \
    -subj "/CN=$MAIL_HOST" >/dev/null 2>&1
  cat > "$TLS_DIR/server.ext" <<EXT
subjectAltName=DNS:$DEMO_HOST,DNS:$MAIL_HOST${PUBLIC_IP:+,IP:$PUBLIC_IP}
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
EXT
  openssl x509 -req -in "$TLS_DIR/server.csr" -CA "$TLS_DIR/demo-ca.pem" \
    -CAkey "$TLS_DIR/demo-ca.key" -CAcreateserial -days 90 \
    -out "$TLS_DIR/fullchain.pem" -extfile "$TLS_DIR/server.ext" >/dev/null 2>&1
  chmod 0640 "$TLS_DIR"/*.pem "$TLS_DIR"/*.key
  chgrp nginx "$TLS_DIR"/*.pem "$TLS_DIR"/*.key || true
fi

log "configuring the internal Docker network and mail proxy"
if ! docker network inspect perch-demo-internal >/dev/null 2>&1; then
  docker network create --driver bridge --internal --subnet 172.31.240.0/24 perch-demo-internal
fi
docker rm -f perch-demo-proxy >/dev/null 2>&1 || true
docker run -d --name perch-demo-proxy --restart unless-stopped \
  --network perch-demo-internal --ip 172.31.240.2 \
  --entrypoint /bin/sh alpine/socat \
  -c 'socat TCP-LISTEN:993,fork,reuseaddr TCP:172.17.0.1:993 & socat TCP-LISTEN:465,fork,reuseaddr TCP:172.17.0.1:465 & wait' >/dev/null
docker network connect bridge perch-demo-proxy

log "building the terminal image"
rm -rf "$INSTALL_DIR/terminal"
mkdir -p "$INSTALL_DIR/terminal"
tar -xzf "$ARTIFACT_DIR/terminal.tar.gz" -C "$INSTALL_DIR/terminal"
cp "$TLS_DIR/demo-ca.pem" "$INSTALL_DIR/terminal/demo-ca.pem"
docker build -t perch-demo-terminal:latest "$INSTALL_DIR/terminal" >/dev/null

STREAM_MOD=$(find /usr/lib64/nginx/modules /usr/share/nginx/modules -name 'ngx_stream_module.so' 2>/dev/null | head -1 || true)
if [ -z "$STREAM_MOD" ]; then
  log "nginx is missing the stream module"
  exit 1
fi
cat > /etc/nginx/nginx.conf <<NGINX
load_module $STREAM_MOD;
worker_processes auto;
error_log /var/log/nginx/error.log warn;
pid /run/nginx.pid;

events { worker_connections 1024; }

http {
  include /etc/nginx/mime.types;
  default_type application/octet-stream;
  log_format main '\$remote_addr - \$remote_user [\$time_local] "\$request" '
                  '\$status \$body_bytes_sent "\$http_referer" "\$http_user_agent"';
  access_log /var/log/nginx/access.log main;
  sendfile on;
  keepalive_timeout 65;
  map \$http_upgrade \$connection_upgrade { default upgrade; '' close; }

  server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name $DEMO_HOST $MAIL_HOST;
    location /.well-known/acme-challenge/ { root /var/www/acme; }
    location / { return 301 https://\$host\$request_uri; }
  }

  server {
    listen 443 ssl;
    http2 on;
    server_name $DEMO_HOST;
    ssl_certificate $TLS_DIR/fullchain.pem;
    ssl_certificate_key $TLS_DIR/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    location / {
      proxy_pass http://127.0.0.1:8080;
      proxy_http_version 1.1;
      proxy_set_header Upgrade \$http_upgrade;
      proxy_set_header Connection \$connection_upgrade;
      proxy_set_header Host \$host;
      proxy_set_header X-Real-IP \$remote_addr;
      proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto https;
      proxy_read_timeout 3600s;
      proxy_send_timeout 3600s;
    }
  }
}

stream {
  server {
    listen 993 ssl;
    ssl_certificate $TLS_DIR/fullchain.pem;
    ssl_certificate_key $TLS_DIR/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    proxy_pass 127.0.0.1:1143;
    proxy_timeout 1h;
  }
  server {
    listen 465 ssl;
    ssl_certificate $TLS_DIR/fullchain.pem;
    ssl_certificate_key $TLS_DIR/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    proxy_pass 127.0.0.1:1587;
    proxy_timeout 1h;
  }
}
NGINX

cat > "$CONFIG_DIR/previewd.env" <<ENV
PREVIEWD_FLAGS=-listen 127.0.0.1:8080 -perch-config $CONFIG_DIR/perch.yaml -perch-bin /usr/local/bin/perch -domain $ACCOUNT_DOMAIN -mail-host $MAIL_HOST -imap-port 993 -smtp-port 465 -ttl 15m -max-sessions 50 -terminal-image perch-demo-terminal:latest -docker-network perch-demo-internal -proxy-ip 172.31.240.2 -state-dir $DATA_DIR -docker-bin /usr/bin/docker -secure-cookie=true
ENV

cat > /etc/systemd/system/perch-demo.service <<UNIT
[Unit]
Description=perch shared live demo server
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
User=perchdemo
Group=perchdemo
ExecStart=/usr/local/bin/perch serve --config $CONFIG_DIR/perch.yaml
Restart=on-failure
RestartSec=2
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/previewd.service <<UNIT
[Unit]
Description=perch live demo session service
After=network-online.target docker.service perch-demo.service
Wants=network-online.target docker.service

[Service]
Type=simple
User=perchdemo
Group=perchdemo
SupplementaryGroups=docker
EnvironmentFile=$CONFIG_DIR/previewd.env
ExecStart=/bin/sh -c 'exec /usr/local/bin/previewd \$PREVIEWD_FLAGS'
Restart=always
RestartSec=2
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
nginx -t
systemctl enable --now nginx
systemctl enable --now perch-demo.service
systemctl enable --now previewd.service

log "services started"
systemctl --no-pager --lines=5 status perch-demo.service previewd.service nginx || true
