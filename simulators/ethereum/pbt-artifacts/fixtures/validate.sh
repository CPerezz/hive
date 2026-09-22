#!/usr/bin/env bash
# The simulator's judgement, against one geth binary and no docker: the valid
# pair must verify, every reject case must be refused for the reason its
# clause names, and the unspecified cases are reported either way.
#
# Usage: ./validate.sh /path/to/geth
set -u

geth="${1:-geth}"
here="$(cd "$(dirname "$0")" && pwd)"
reasons="$here/../shims/go-ethereum.reasons.json"
datadir="$(mktemp -d)"
trap 'rm -rf "$datadir"' EXIT

"$geth" --datadir "$datadir" init "$here/genesis.json" >/dev/null 2>&1 || { echo "FATAL: genesis init failed"; exit 2; }

verify() { # snapshot preimages -> stderr on stdout, exit status
    "$geth" --datadir "$datadir" bintrie import --verify-only "$here/$1" "$here/$2" 0 2>&1 </dev/null
}

pass=0 fail=0
out=$(verify valid/snapshot.bin valid/preimages.bin)
if [ $? -eq 0 ]; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL valid pair: rejected"; fi

while IFS=$'\t' read -r id expect snapshot preimages; do
    out=$(verify "$snapshot" "$preimages")
    status=$?
    if [ "$expect" = "unspecified" ]; then
        echo "note $id: exit $status (clause is open; not scored)"
        continue
    fi
    if [ $status -eq 0 ]; then
        fail=$((fail + 1)); echo "FAIL $id: accepted"; continue
    fi
    want=$(jq -r --arg id "$id" '.[$id] // empty' "$reasons")
    if [ -n "$want" ] && ! echo "$out" | grep -qE "$want"; then
        fail=$((fail + 1)); echo "FAIL $id: rejected for another reason than /$want/"; echo "$out" | tail -1; continue
    fi
    pass=$((pass + 1))
done < <(jq -r '.cases[] | [.id, .expect, .snapshot, .preimages] | @tsv' "$here/manifest.json")

echo "--- $pass passed, $fail failed"
[ "$fail" -eq 0 ]
