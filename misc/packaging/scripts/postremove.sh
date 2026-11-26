#!/bin/sh
# Nothing that cannot be regenerated is removed, not even by "apt purge": the
# database (/var/lib/xolo) and /etc/xolo/secret.env go together, the key in
# the second one being the only way to read the provider API keys of the first.
# The xolo account stays as well, as system accounts do on both families of
# distributions: files may still belong to it.
set -e

if [ "$1" = "purge" ]; then
    # dpkg kept the service enabled on removal (see preremove.sh); a purge
    # forgets it, the unit file being already gone.
    rm -f /etc/systemd/system/multi-user.target.wants/xolo.service
    rm -rf /var/cache/xolo
fi

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi
