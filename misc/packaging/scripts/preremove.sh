#!/bin/sh
# rpm passes the number of versions left (0 on removal, 1 on upgrade), dpkg
# passes "remove" or "upgrade". The service is stopped on removal only: an
# upgrade restarts it from the post-installation script. Each format keeps its
# own convention for the enablement: rpm disables the service on removal, as
# %systemd_preun does; dpkg keeps it enabled until a purge, so that a package
# removed and installed again comes back up at the next boot.
set -e

[ -d /run/systemd/system ] || exit 0

case "$1" in
    0)
        systemctl disable --now xolo.service >/dev/null 2>&1 || true
        ;;
    remove)
        systemctl stop xolo.service >/dev/null 2>&1 || true
        ;;
esac
