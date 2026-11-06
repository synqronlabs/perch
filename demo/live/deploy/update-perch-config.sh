#!/bin/bash
# Install a new shared perch config and restart the demo server.
set -euo pipefail
cfg="${1:?usage: update-perch-config.sh /path/to/perch.yaml}"
install -m 0600 -o perchdemo -g perchdemo "$cfg" /etc/perch-demo/perch.yaml
systemctl restart perch-demo.service
systemctl --no-pager --lines=20 status perch-demo.service
