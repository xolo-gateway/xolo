#!/usr/bin/env bash
# Posts a release announcement, with its changelog, to a Matrix room.
#
# Reads the release notes goreleaser published on GitHub for the given tag, so
# the message carries exactly the changelog shown on the release page.
#
# Required environment:
#   GH_TOKEN             token able to read the release (GITHUB_TOKEN is enough)
#   MATRIX_ACCESS_TOKEN  access token of the announcing account, already
#                        invited to or able to join the room
#   MATRIX_ROOM          room alias (#xolo-announces:matrix.org) or room ID
# Optional:
#   MATRIX_HOMESERVER    base URL of the homeserver (default https://matrix.org)
#   MATRIX_TXN_ID        transaction ID for idempotent sends (default: tag + time)
#
# Usage: announce-matrix.sh <tag>
set -euo pipefail

tag="${1:?usage: $0 <tag>}"
homeserver="${MATRIX_HOMESERVER:-https://matrix.org}"
room="${MATRIX_ROOM:?MATRIX_ROOM is required}"
: "${MATRIX_ACCESS_TOKEN:?MATRIX_ACCESS_TOKEN is required}"

api() {
  local method="$1" path="$2"
  shift 2
  curl --fail-with-body --silent --show-error \
    -X "$method" \
    -H "Authorization: Bearer ${MATRIX_ACCESS_TOKEN}" \
    -H "Content-Type: application/json" \
    "$@" \
    "${homeserver}/_matrix/client/v3${path}"
}

urlencode() { jq -rn --arg v "$1" '$v|@uri'; }

# 1. Release notes as published by goreleaser.
release_json="$(gh release view "$tag" --json name,url,body)"
name="$(jq -r '.name' <<<"$release_json")"
url="$(jq -r '.url' <<<"$release_json")"
body="$(jq -r '.body' <<<"$release_json")"

# 2. Two renderings: plain text (Markdown, readable by every client) and HTML.
#    GitHub's Markdown API renders the GFM changelog, which spares the runner a
#    pandoc install; `context` linkifies the #123 and @user references it holds.
markdown="$(printf '## Xolo %s est disponible\n\n%s\n\n%s\n' "$name" "$body" "$url")"
html="$(gh api --method POST /markdown \
  -f text="$markdown" \
  -f mode=gfm \
  ${GITHUB_REPOSITORY:+-f context="$GITHUB_REPOSITORY"})"

# 3. Resolve the alias, make sure the account is in the room.
if [[ "$room" == \#* ]]; then
  room_id="$(api GET "/directory/room/$(urlencode "$room")" | jq -r '.room_id')"
else
  room_id="$room"
fi
api POST "/join/$(urlencode "$room_id")" --data '{}' >/dev/null

# 4. Send. PUT with a transaction ID makes a retried job idempotent.
txn_id="${MATRIX_TXN_ID:-${tag}-$(date +%s)}"
payload="$(jq -n --arg body "$markdown" --arg html "$html" '{
  msgtype: "m.notice",
  body: $body,
  format: "org.matrix.custom.html",
  formatted_body: $html
}')"
event_id="$(api PUT "/rooms/$(urlencode "$room_id")/send/m.room.message/$(urlencode "$txn_id")" --data "$payload" | jq -r '.event_id')"
echo "announced ${tag} in ${room} (${event_id})"
