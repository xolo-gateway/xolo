#!/bin/sh
# Runs before the files are unpacked: /etc/xolo/xolo.env belongs to the xolo
# group, which must exist by then. That is why the account is created here and
# not from /usr/lib/sysusers.d/xolo.conf, unpacked later; that file keeps the
# account declared on image-based systems, which create it at boot.
set -e

if getent passwd xolo >/dev/null && getent group xolo >/dev/null; then
    exit 0
fi

if command -v systemd-sysusers >/dev/null 2>&1; then
    # Same line as misc/packaging/xolo.sysusers.
    echo 'u xolo - "Xolo LLM gateway" /var/lib/xolo' | systemd-sysusers --replace=/usr/lib/sysusers.d/xolo.conf -
    exit 0
fi

if ! getent group xolo >/dev/null; then
    groupadd --system xolo
fi

if ! getent passwd xolo >/dev/null; then
    nologin=/usr/sbin/nologin
    [ -x "$nologin" ] || nologin=/sbin/nologin
    useradd --system --gid xolo --home-dir /var/lib/xolo --no-create-home \
        --shell "$nologin" --comment "Xolo LLM gateway" xolo
fi
