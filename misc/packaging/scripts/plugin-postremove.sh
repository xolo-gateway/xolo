#!/bin/sh
# A removed plugin stays loaded until the server restarts.
#
# dpkg: the trigger of the xolo package covers removals too.
# rpm passes 0 on removal, 1 on upgrade (the post-transaction script of the new
# version restarts). rpm runs no post-transaction script for an erased package,
# and a restart per package trips the start limit of systemd when several are
# removed at once: the first removal schedules a single restart a few seconds
# later, under a fixed unit name that the next ones find already taken.
set -e

if [ "$1" = 0 ] && [ -d /run/systemd/system ]; then
    # A timer fires up to AccuracySec (1 min by default) after its time.
    systemd-run --quiet --collect --unit=xolo-plugins-restart --on-active=5s \
        --timer-property=AccuracySec=1s \
        /usr/bin/systemctl try-restart xolo.service >/dev/null 2>&1 || true
fi
