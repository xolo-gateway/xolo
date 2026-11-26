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

[ -d /run/systemd/system ] || exit 0

systemctl daemon-reload >/dev/null 2>&1 || true

# The service is neither enabled nor started by the package: it needs an
# identity provider first. A running one is restarted on the new version,
# once per command even when plugin packages change with it:
case "$1" in
    configure)
        # dpkg: the restart goes through the trigger, which dpkg runs once
        # for this activation and those of the plugin packages.
        dpkg-trigger --no-await xolo-restart 2>/dev/null ||
            systemctl try-restart xolo.service >/dev/null 2>&1 || true
        ;;
    triggered)
        systemctl try-restart xolo.service >/dev/null 2>&1 || true
        ;;
    [0-9]*)
        # rpm: the restart is left to the post-transaction script.
        mkdir -p /run/xolo-packages
        : >/run/xolo-packages/restart
        ;;
esac
