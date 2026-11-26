#!/bin/sh
# rpm only. Runs once per installed plugin package at the end of the
# transaction: the first one restarts the server, the others find the marker
# gone.
set -e

marker=/run/xolo-packages/plugins-changed
if [ -e "$marker" ]; then
    rm -f "$marker"
    if [ -d /run/systemd/system ]; then
        systemctl try-restart xolo.service >/dev/null 2>&1 || true
    fi
fi
