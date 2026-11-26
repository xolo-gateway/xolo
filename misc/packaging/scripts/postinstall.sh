#!/bin/sh
# Runs after an installation and after an upgrade, on both package formats.
set -e

secret=/etc/xolo/secret.env

random_hex() {
    od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'
}

# Never replaced once written: XOLO_SECRET_KEY encrypts the provider API keys
# held in the database, a new one would make them unreadable.
if [ ! -e "$secret" ]; then
    umask 077
    {
        echo "# Generated on the first installation of the xolo package. Back it up."
        echo "XOLO_SECRET_KEY=$(random_hex 32)"
        echo "XOLO_HTTP_SESSION_KEYS=$(random_hex 16)"
    } >"$secret.tmp"
    chown root:xolo "$secret.tmp"
    chmod 0640 "$secret.tmp"
    mv "$secret.tmp" "$secret"
fi

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    # The service is neither enabled nor started by the package: it needs an
    # identity provider first. A running one is restarted on the new version.
    systemctl try-restart xolo.service >/dev/null 2>&1 || true
fi
