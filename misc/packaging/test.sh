#!/usr/bin/env bash
# Installs the packages built by goreleaser (the server, every plugin package
# and xolo-plugin-all) on each supported distribution and
# checks what an administrator would: the service starts under systemd, answers,
# loads its plugins, survives a reinstallation and leaves its data on removal.
#
#   make packages        # builds dist/*.rpm and dist/*.deb
#   make test-packages   # runs this script
#
# Each distribution runs in a container with systemd as PID 1, which needs a
# privileged container. See misc/packaging/README.md.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
dist=${1:-"$here/../../dist"}
dist=$(cd "$dist" && pwd)

# name|format|base image|what the base image lacks to boot systemd and probe
targets=(
  "rhel10|rpm|registry.access.redhat.com/ubi10/ubi-init:latest|"
  "rhel9|rpm|registry.access.redhat.com/ubi9/ubi-init:latest|"
  "debian13|deb|debian:13|apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv curl ca-certificates"
  "debian12|deb|debian:12|apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv curl ca-certificates"
)

case "$(uname -m)" in
  x86_64) rpm_arch=x86_64 deb_arch=amd64 ;;
  aarch64) rpm_arch=aarch64 deb_arch=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 2 ;;
esac

only=${XOLO_PACKAGE_TARGETS:-}
failed=()

for target in "${targets[@]}"; do
  IFS='|' read -r name format base setup <<<"$target"
  if [ -n "$only" ] && [[ ",$only," != *",$name,"* ]]; then
    continue
  fi

  if [ "$format" = rpm ]; then arch=$rpm_arch; else arch=$deb_arch; fi
  packages=$(mktemp -d)
  # The metapackage carries no binary: it may be built for no architecture.
  for a in "$arch" noarch all; do
    find "$dist" -maxdepth 1 -name "xolo*[._-]$a.$format" -exec cp {} "$packages/" \;
  done
  for required in xolo xolo-plugin-all; do
    if ! find "$packages" -name "${required}[_-][0-9]*" | grep -q .; then
      echo "no $required $format package for $arch in $dist" >&2
      exit 2
    fi
  done
  chmod -R a+rX "$packages"

  image=xolo-package-test:$name
  echo "=== $name: preparing $base"
  docker build -q -t "$image" - >/dev/null <<DOCKERFILE
FROM $base
RUN ${setup:-true}
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
DOCKERFILE

  container=$(docker run -d --rm --privileged --cgroupns=host \
    --tmpfs /run --tmpfs /tmp \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    -v "$packages:/packages:ro" \
    -v "$here/test/check.sh:/check.sh:ro" \
    "$image")

  echo "=== $name: checking"
  if docker exec "$container" sh /check.sh "$format"; then
    echo "=== $name: ok"
  else
    echo "=== $name: FAILED"
    failed+=("$name")
  fi

  docker kill "$container" >/dev/null 2>&1 || true
  rm -rf "$packages"
done

if [ "${#failed[@]}" -gt 0 ]; then
  echo "failed: ${failed[*]}" >&2
  exit 1
fi
