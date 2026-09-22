#!/usr/bin/env bash
# The simulator's judgement, against one geth binary and no docker: the valid
# pair must verify, every reject case must be refused, and the unspecified
# cases are reported either way.
#
# Usage: ./validate.sh /path/to/geth
set -u

geth="${1:-geth}"
here="$(cd "$(dirname "$0")" && pwd)"
datadir="$(mktemp -d)"
trap 'rm -rf "$datadir"' EXIT

missing=$(jq -r '[.cases[] | select(.effect == null) | .id] | join(" ")' "$here/manifest.json")
[ -z "$missing" ] || { echo "FATAL: cases without a recorded effect: $missing"; exit 2; }

"$geth" --datadir "$datadir" init "$here/genesis.json" >/dev/null 2>&1 || { echo "FATAL: genesis init failed"; exit 2; }

verify() { # snapshot preimages -> exit status
    "$geth" --datadir "$datadir" bintrie import --verify-only "$here/$1" "$here/$2" 0 >/dev/null 2>&1 </dev/null
}

pass=0 fail=0
if verify valid/snapshot.bin valid/preimages.bin; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL valid pair: rejected"; fi

while IFS=$'\t' read -r id expect snapshot preimages; do
    verify "$snapshot" "$preimages"
    status=$?
    if [ "$expect" = "unspecified" ]; then
        echo "note $id: exit $status (clause is open; not scored)"
        continue
    fi
    if [ $status -eq 0 ]; then
        fail=$((fail + 1)); echo "FAIL $id: accepted"; continue
    fi
    pass=$((pass + 1))
done < <(jq -r '.cases[] | [.id, .expect, .snapshot, .preimages] | @tsv' "$here/manifest.json")

echo "--- $pass passed, $fail failed"
[ "$fail" -eq 0 ]
