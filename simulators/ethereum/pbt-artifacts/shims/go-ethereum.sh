#!/bin/bash
# go-ethereum's side of the EIP-8347 artifact contract.
#
#   genesis-root                            -> mpt_root=0x...
#   verify  <snapshot> <preimages> <anchor> -> root=0x...      exit 0 accept / 1 reject
#   convert <anchor>                        -> root=0x... snapshot=<b64> preimages=<b64>
#   import  <snapshot> <preimages> <anchor> -> root=0x...
#
# Artifact paths are relative to the fixture tar. Every verb works on a
# datadir of its own, so the running node's database is never touched and the
# verbs cannot interfere with each other: verify writes nothing, convert
# leaves converted state behind, import leaves imported state behind.
#
# Exit 3 means unsupported, and anything other than 0 or 1 is a crash. The
# client's own status is echoed as client_exit= and never swallowed.
set -u

GETH=/usr/local/bin/geth
FIXTURES=/pbt/fixtures
verb="${1:-}"; shift || true

unpack() {
    [ -d "$FIXTURES" ] && return 0
    mkdir -p "$FIXTURES" || return 1
    tar -xf /pbt-fixtures.tar -C "$FIXTURES"
}

# init_datadir builds a throwaway chain from the same genesis the node booted
# from. $1 is the datadir, $2 "preimages" when the converter will read it: the
# MPT is hash-keyed, so a conversion needs the plain keys kept.
init_datadir() {
    local dir="$1" preimages="${2:-}" flags=()
    [ -d "$dir" ] && return 0
    [ "$preimages" = "preimages" ] && flags+=(--cache.preimages)
    "$GETH" --datadir "$dir" "${flags[@]}" init /genesis.json >/dev/null 2>&1
}

# claimed_root prints the PBT root from the snapshot header, which is the
# value the client just checked its own rebuild against.
claimed_root() {
    echo "root=0x$(od -An -tx1 -N32 "$1" | tr -d ' \n')"
}

case "$verb" in
genesis-root)
    # Asked of the node itself over JSON-RPC, so this reports the state the
    # client actually built from the genesis it was given, not a re-derivation.
    for _ in $(seq 1 60); do
        root=$(curl -s -X POST -H 'Content-Type: application/json' \
            --data '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",false]}' \
            http://127.0.0.1:8545 2>/dev/null | jq -r '.result.stateRoot // empty')
        if [ -n "$root" ]; then
            echo "mpt_root=$root"
            exit 0
        fi
        sleep 1
    done
    echo "the node's RPC never answered eth_getBlockByNumber(0x0)" >&2
    exit 2
    ;;

verify)
    unpack || { echo "cannot unpack the fixtures" >&2; exit 2; }
    snapshot="$FIXTURES/$1"; preimages="$FIXTURES/$2"; anchor="$3"
    init_datadir /pbt/dd-verify || { echo "genesis init failed" >&2; exit 2; }

    out=$("$GETH" --datadir /pbt/dd-verify bintrie import --verify-only \
        "$snapshot" "$preimages" "$anchor" 2>&1)
    status=$?
    echo "client_exit=$status"
    if [ $status -eq 0 ]; then
        claimed_root "$snapshot"
        exit 0
    fi
    echo "$out" >&2
    exit 1
    ;;

convert)
    unpack || { echo "cannot unpack the fixtures" >&2; exit 2; }
    init_datadir /pbt/dd-convert preimages || { echo "genesis init failed" >&2; exit 2; }

    out=$("$GETH" --datadir /pbt/dd-convert bintrie convert --force \
        --snapshot-out /pbt/out-snapshot.bin --preimages-out /pbt/out-preimages.bin 2>&1)
    status=$?
    echo "client_exit=$status"
    if [ $status -ne 0 ]; then
        echo "$out" >&2
        exit 1
    fi
    claimed_root /pbt/out-snapshot.bin
    echo "snapshot=$(base64 -w0 /pbt/out-snapshot.bin 2>/dev/null || base64 /pbt/out-snapshot.bin | tr -d '\n')"
    echo "preimages=$(base64 -w0 /pbt/out-preimages.bin 2>/dev/null || base64 /pbt/out-preimages.bin | tr -d '\n')"
    exit 0
    ;;

import)
    unpack || { echo "cannot unpack the fixtures" >&2; exit 2; }
    snapshot="$FIXTURES/$1"; preimages="$FIXTURES/$2"; anchor="$3"
    init_datadir /pbt/dd-import || { echo "genesis init failed" >&2; exit 2; }

    out=$("$GETH" --datadir /pbt/dd-import bintrie import --force \
        "$snapshot" "$preimages" "$anchor" 2>&1)
    status=$?
    echo "client_exit=$status"
    if [ $status -eq 0 ]; then
        claimed_root "$snapshot"
        exit 0
    fi
    echo "$out" >&2
    exit 1
    ;;

*)
    echo "unknown verb: $verb" >&2
    exit 2
    ;;
esac
