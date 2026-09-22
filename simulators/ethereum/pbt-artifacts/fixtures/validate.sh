#!/usr/bin/env bash
# Checks the fixture set against a reference implementation, outside hive:
# the valid pair must verify, every "reject" case must be refused, and the
# "unspecified" cases are reported either way. This is the same judgement the
# simulator makes, run locally against one binary.
#
# Usage: ./validate.sh /path/to/geth
set -u

geth="${1:-geth}"
here="$(cd "$(dirname "$0")" && pwd)"
datadir="$(mktemp -d)"
trap 'rm -rf "$datadir"' EXIT

if ! "$geth" --datadir "$datadir" init "$here/genesis.json" >/dev/null 2>&1; then
    echo "FATAL: genesis init failed"
    exit 2
fi

verify() { # snapshot preimages -> exit status
    "$geth" --datadir "$datadir" bintrie import --verify-only "$here/$1" "$here/$2" 0 >/dev/null 2>&1
}

pass=0 fail=0
report() { # expected actual id
    if [ "$1" = "$2" ]; then
        pass=$((pass + 1))
    else
        fail=$((fail + 1))
        echo "FAIL $3: expected $1, got $2"
    fi
}

verify valid/snapshot.bin valid/preimages.bin
report accept "$([ $? -eq 0 ] && echo accept || echo reject)" "valid pair"

while IFS=$'\t' read -r id expect snapshot preimages; do
    verify "$snapshot" "$preimages"
    got=$([ $? -eq 0 ] && echo accept || echo reject)
    if [ "$expect" = "unspecified" ]; then
        echo "note $id: $got (clause is open; not scored)"
        continue
    fi
    report "$expect" "$got" "$id"
done < <(jq -r '.cases[] | [.id, .expect, .snapshot, .preimages] | @tsv' "$here/manifest.json")

echo "--- $pass passed, $fail failed"
[ "$fail" -eq 0 ]
