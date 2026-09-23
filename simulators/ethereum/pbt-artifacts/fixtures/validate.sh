#!/usr/bin/env bash
# The simulator's judgement, against one geth binary and no docker: the valid
# pair must verify, every reject case must be refused, and the unspecified
# cases are reported either way. A producer case must make the converter
# refuse its source for a missing preimage. First, the admission gates every
# canonical pair passed: the strict decoder always, the spec-reference root
# when an execution-specs checkout is given.
#
# Usage: ./validate.sh /path/to/geth [/path/to/execution-specs]
set -u

geth="${1:-geth}"
specs="${2:-}"
here="$(cd "$(dirname "$0")" && pwd)"
datadir="$(mktemp -d)"
trap 'rm -rf "$datadir"' EXIT

missing=$(jq -r '[.cases[] | select(.suite != "produce" and .effect == null) | .id] | join(" ")' "$here/manifest.json")
[ -z "$missing" ] || { echo "FATAL: cases without a recorded effect: $missing"; exit 2; }

(
    cd "$here/gen" || exit 2
    [ -f go.mod ] || { cp go.mod.dist go.mod && cp go.sum.dist go.sum; } || exit 2
    go run . -check -out .. ${specs:+-ref "$specs"}
) || { echo "FATAL: an admission gate does not hold"; exit 2; }

"$geth" --datadir "$datadir" init "$here/genesis.json" >/dev/null 2>&1 || { echo "FATAL: genesis init failed"; exit 2; }

verify() { # snapshot preimages -> exit status
    "$geth" --datadir "$datadir" bintrie import --verify-only "$here/$1" "$here/$2" 0 >/dev/null 2>&1 </dev/null
}

# produce converts a fresh source after applying the case's defect: each
# drop-preimage <hash> deletes the store key "secure-key-" + hash.
produce() {
    local dir status
    dir="$(mktemp -d)"
    "$geth" --datadir "$dir" --cache.preimages init "$here/genesis.json" >/dev/null 2>&1 || { rm -rf "$dir"; return 2; }
    while [ $# -ge 2 ]; do
        key=0x7365637572652d6b65792d${2#0x}
        { "$geth" --datadir "$dir" db get "$key" && "$geth" --datadir "$dir" db delete "$key"; } >/dev/null 2>&1 || { rm -rf "$dir"; return 2; }
        shift 2
    done
    out=$("$geth" --datadir "$dir" bintrie convert --snapshot-out "$dir/s.bin" --preimages-out "$dir/p.bin" 2>&1 </dev/null)
    status=$?
    rm -rf "$dir"
    [ $status -eq 0 ] && return 0
    echo "$out" | grep -q 'missing preimage for' && return 1
    return 2
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
done < <(jq -r '.cases[] | select(.suite != "produce") | [.id, .expect, .snapshot, .preimages] | @tsv' "$here/manifest.json")

while IFS=$'\t' read -r id defect; do
    # shellcheck disable=SC2086 # the defect is an argument list
    produce $defect
    case $? in
    1) pass=$((pass + 1)) ;;
    0) fail=$((fail + 1)); echo "FAIL $id: converted" ;;
    *) fail=$((fail + 1)); echo "FAIL $id: the converter or the defect crashed" ;;
    esac
done < <(jq -r '.cases[] | select(.suite == "produce") | [.id, (.defect | join(" "))] | @tsv' "$here/manifest.json")

echo "--- $pass passed, $fail failed"
[ "$fail" -eq 0 ]
