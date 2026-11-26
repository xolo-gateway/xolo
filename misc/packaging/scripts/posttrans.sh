#!/bin/sh
# rpm only, in the xolo package and in every plugin package. Runs at the end
# of the transaction, once per installed package: the first one to find the
# marker that their post-installation scripts leave restarts the server, the
# others find it gone. One restart whatever the number of packages, xolo
# included.
set -e

marker=/run/xolo-packages/restart
if [ -e "$marker" ]; then
    rm -f "$marker"
    if [ -d /run/systemd/system ]; then
        systemctl try-restart xolo.service >/dev/null 2>&1 || true
    fi
fi
