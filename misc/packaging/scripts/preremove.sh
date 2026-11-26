#!/bin/sh
# rpm passes the number of versions left (0 on removal, 1 on upgrade), dpkg
# passes "remove" or "upgrade". The service is stopped on removal only: an
# upgrade restarts it from the post-installation script.
set -e

case "$1" in
    0 | remove)
        if [ -d /run/systemd/system ]; then
            systemctl disable --now xolo.service >/dev/null 2>&1 || true
        fi
        ;;
esac
