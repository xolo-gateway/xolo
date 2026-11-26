#!/bin/sh
# The server lists its plugins at startup only, so it must restart once the
# plugin packages are in place, and once only when several are installed
# together.
#
# dpkg passes "configure": the trigger that the xolo package declares on
# /usr/lib/xolo/plugins restarts the server, nothing to do here.
# rpm passes the number of versions installed: the restart is left to the
# post-transaction script, which runs after every package of the transaction.
set -e

case "$1" in
    [0-9]*)
        mkdir -p /run/xolo-packages
        : >/run/xolo-packages/plugins-changed
        ;;
esac
