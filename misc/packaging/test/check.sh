#!/bin/sh
# Runs inside a container booted on systemd, as root, with the packages of
# xolo, of every plugin and xolo-plugin-all under /packages. Called by misc/packaging/test.sh.
set -eu

format=$1
url=http://127.0.0.1:3002

step() { echo "--- $*"; }
fail() {
    echo "FAIL: $*" >&2
    journalctl -u xolo --no-pager -n 40 >&2 2>/dev/null || true
    exit 1
}

install_packages() {
    if [ "$format" = rpm ]; then
        dnf install -y --disablerepo='*' /packages/*.rpm >/dev/null
    else
        DEBIAN_FRONTEND=noninteractive apt-get install -y /packages/*.deb >/dev/null
    fi
}

reinstall_packages() {
    if [ "$format" = rpm ]; then
        dnf reinstall -y --disablerepo='*' /packages/*.rpm >/dev/null
    else
        DEBIAN_FRONTEND=noninteractive apt-get install -y --reinstall /packages/*.deb >/dev/null
    fi
}

plugin_packages() {
    if [ "$format" = rpm ]; then
        rpm -qa --qf '%{NAME}\n' 'xolo-plugin-*'
    else
        dpkg-query -W -f '${db:Status-Status} ${Package}\n' 'xolo-plugin-*' 2>/dev/null |
            awk '$1 == "installed" { print $2 }'
    fi
}

remove() {
    if [ "$format" = rpm ]; then
        dnf remove -y "$@" >/dev/null
    else
        DEBIAN_FRONTEND=noninteractive apt-get remove -y "$@" >/dev/null
    fi
}

# Installs the plugin packages under /packages, every one of them.
install_plugins() {
    if [ "$format" = rpm ]; then
        dnf install -y --disablerepo='*' /packages/xolo-plugin-*.rpm >/dev/null
    else
        DEBIAN_FRONTEND=noninteractive apt-get install -y /packages/xolo-plugin-*.deb >/dev/null
    fi
}

# Waits up to 30 s for the server to restart after the start number $1, then
# for it to answer.
wait_restart() {
    i=0
    until [ "$(starts)" -gt "$1" ]; do
        i=$((i + 1))
        [ "$i" -lt 30 ] || fail "the server was not restarted"
        sleep 1
    done
    wait_ready
}

# Number of times the server started, read from its journal.
starts() {
    journalctl -u xolo --no-pager | grep -c 'use ctrl+c to interrupt' || true
}

# Plugins loaded by the running server, since its last start.
loaded_plugins() {
    invocation=$(systemctl show -p InvocationID --value xolo)
    journalctl --no-pager _SYSTEMD_INVOCATION_ID="$invocation" | grep -c 'plugin loaded' || true
}

wait_ready() {
    i=0
    until curl -fsS "$url/readyz" >/dev/null 2>&1; do
        i=$((i + 1))
        [ "$i" -lt 60 ] || fail "the service does not answer on /readyz"
        sleep 1
    done
}

# systemd answers before it has finished booting.
i=0
until systemctl is-system-running 2>/dev/null | grep -Eq 'running|degraded'; do
    i=$((i + 1))
    [ "$i" -lt 60 ] || fail "systemd did not finish booting"
    sleep 1
done

step "installation"
install_packages

step "account, files and permissions"
[ "$(id -gn xolo)" = xolo ] || fail "no xolo account"
[ "$(stat -c '%a %U:%G' /etc/xolo/xolo.env)" = "640 root:xolo" ] || fail "xolo.env: $(stat -c '%a %U:%G' /etc/xolo/xolo.env)"
[ "$(stat -c '%a %U:%G' /var/lib/xolo)" = "750 xolo:xolo" ] || fail "/var/lib/xolo: $(stat -c '%a %U:%G' /var/lib/xolo)"
[ "$(stat -c '%a %U:%G' /etc/xolo/secret.env)" = "640 root:xolo" ] || fail "secret.env: $(stat -c '%a %U:%G' /etc/xolo/secret.env)"
grep -Eq '^XOLO_SECRET_KEY=[0-9a-f]{64}$' /etc/xolo/secret.env || fail "no generated XOLO_SECRET_KEY"
grep -Eq '^XOLO_HTTP_SESSION_KEYS=[0-9a-f]{32}$' /etc/xolo/secret.env || fail "no generated XOLO_HTTP_SESSION_KEYS"
for binary in xolo-server xolo-migrate xolo-adoption xolo-admin; do
    [ -x "/usr/bin/$binary" ] || fail "missing /usr/bin/$binary"
done
plugins=$(find /usr/lib/xolo/plugins -maxdepth 1 -type f -perm -u+x | wc -l)
[ "$plugins" -gt 0 ] || fail "no plugin installed"
[ "$(plugin_packages | grep -vc '^xolo-plugin-all$')" -eq "$plugins" ] || fail "not one package per plugin"
systemd-analyze verify /usr/lib/systemd/system/xolo.service || fail "systemd rejects the unit"

step "the package neither enables nor starts the service"
! systemctl is-enabled --quiet xolo || fail "enabled by the package"
! systemctl is-active --quiet xolo || fail "started by the package"

step "start"
systemctl enable --now xolo >/dev/null 2>&1 || fail "the service does not start"
wait_ready
curl -fsS "$url/healthz" >/dev/null || fail "/healthz does not answer"
# Read from /proc: the Debian images have no ps.
[ "$(stat -c %U "/proc/$(systemctl show -p MainPID --value xolo)")" = xolo ] || fail "the server does not run as xolo"
[ -s /var/lib/xolo/data.sqlite ] || fail "no database under /var/lib/xolo"

step "xolo-admin runs the offline commands with the service configuration"
xolo-admin migrate diagnose >/tmp/diagnose.json || fail "xolo-admin migrate diagnose failed"
[ "$(stat -c %U /var/lib/xolo/data.sqlite)" = xolo ] || fail "the database does not belong to xolo"
xolo-admin adoption export -out /var/lib/xolo/inventory.json || fail "xolo-admin adoption export failed"
[ "$(stat -c %U /var/lib/xolo/inventory.json)" = xolo ] || fail "the export does not belong to xolo"
xolo-admin adoption verify -in /var/lib/xolo/inventory.json >/dev/null || fail "xolo-admin adoption verify failed"
rm -f /var/lib/xolo/inventory.json

step "plugins"
loaded=$(loaded_plugins)
[ "$loaded" -eq "$plugins" ] || fail "$loaded plugins loaded out of $plugins"

# On RPM the restart that follows a removal is delayed by a few seconds.
step "removing a plugin restarts the server without it"
before=$(starts)
remove xolo-plugin-dummy-model
[ ! -e /usr/lib/xolo/plugins/xolo-plugin-dummy-model ] || fail "plugin left behind"
wait_restart "$before"
loaded=$(loaded_plugins)
[ "$loaded" -eq $((plugins - 1)) ] || fail "$loaded plugins loaded out of $((plugins - 1))"

step "removing every plugin restarts the server once, without them"
before=$(starts)
remove $(plugin_packages)
[ -z "$(plugin_packages)" ] || fail "plugin packages left: $(plugin_packages)"
wait_restart "$before"
sleep 7 # a second delayed restart would have fired by now
systemctl is-active --quiet xolo || fail "the server stopped with its plugins"
[ "$(starts)" -eq $((before + 1)) ] || fail "$(($(starts) - before)) restarts for one removal"
[ "$(loaded_plugins)" -eq 0 ] || fail "plugins still loaded"

step "installing every plugin restarts the server once"
before=$(starts)
install_plugins
wait_ready
[ "$(starts)" -eq $((before + 1)) ] || fail "$(($(starts) - before)) restarts for one installation"
loaded=$(loaded_plugins)
[ "$loaded" -eq "$plugins" ] || fail "$loaded plugins loaded out of $plugins"

step "reinstallation keeps the secrets and restarts the service"
secret=$(cat /etc/xolo/secret.env)
pid=$(systemctl show -p MainPID --value xolo)
echo "XOLO_HTTP_BASE_URL=http://changed.example.com" >>/etc/xolo/xolo.env
reinstall_packages
[ "$(cat /etc/xolo/secret.env)" = "$secret" ] || fail "secret.env was regenerated"
grep -q changed.example.com /etc/xolo/xolo.env || fail "xolo.env was replaced"
wait_ready
[ "$(systemctl show -p MainPID --value xolo)" != "$pid" ] || fail "the service was not restarted"

step "removal stops the service and keeps the data"
remove xolo
[ -z "$(plugin_packages)" ] || fail "plugin packages left without xolo: $(plugin_packages)"
! systemctl is-active --quiet xolo || fail "still running after removal"
[ ! -e /usr/bin/xolo-server ] || fail "binary left behind"
[ "$(cat /etc/xolo/secret.env)" = "$secret" ] || fail "secret.env was removed"
[ -s /var/lib/xolo/data.sqlite ] || fail "the database was removed"

echo "all checks passed"
