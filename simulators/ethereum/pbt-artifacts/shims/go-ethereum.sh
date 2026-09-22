#!/bin/bash
# go-ethereum. Every verb works on a datadir of its own, so the running node
# is never touched.
set -u
. /hive-bin/pbt-common.sh

GETH=/usr/local/bin/geth
verb="${1:-}"; shift || true

# init_datadir builds a chain from the same genesis the node booted from.
init_datadir() {
    local dir="$1"; shift
    [ -d "$dir" ] || "$GETH" --datadir "$dir" "$@" init /genesis.json >/dev/null 2>&1
}

case "$verb" in
genesis-root)
    genesis_root
    ;;

verify)
    unpack || { echo "cannot unpack the fixtures" >&2; exit 2; }
    init_datadir /pbt/dd-verify || { echo "genesis init failed" >&2; exit 2; }
    out=$("$GETH" --datadir /pbt/dd-verify bintrie import --verify-only "$FIXTURES/$1" "$FIXTURES/$2" "$3" 2>&1)
    status=$?
    echo "client_exit=$status"
    [ $status -eq 0 ] && exit 0
    echo "$out" >&2
    exit 1
    ;;

convert)
    # The converter reads the plain keys behind the trie paths, so the state
    # is written with the preimage store on.
    init_datadir /pbt/dd-convert --cache.preimages || { echo "genesis init failed" >&2; exit 2; }
    out=$("$GETH" --datadir /pbt/dd-convert bintrie convert --force \
        --snapshot-out /pbt/snapshot.bin --preimages-out /pbt/preimages.bin 2>&1)
    status=$?
    echo "client_exit=$status"
    if [ $status -ne 0 ]; then
        echo "$out" >&2
        exit 1
    fi
    echo "snapshot=$(b64 /pbt/snapshot.bin)"
    echo "preimages=$(b64 /pbt/preimages.bin)"
    ;;

*)
    echo "unknown verb: $verb" >&2
    exit 2
    ;;
esac
