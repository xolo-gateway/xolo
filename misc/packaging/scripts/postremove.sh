#!/bin/sh
# Nothing that cannot be regenerated is removed, not even by "apt purge": the
# database (/var/lib/xolo) and /etc/xolo/secret.env go together, the key in
# the second one being the only way to read the provider API keys of the first.
set -e

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

if [ "$1" = "purge" ]; then
    rm -rf /var/cache/xolo
fi
