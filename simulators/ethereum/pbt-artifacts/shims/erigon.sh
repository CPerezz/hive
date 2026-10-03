#!/bin/bash
set -u
. /hive-bin/pbt-common.sh

ERIGON=/usr/local/bin/erigon
verb="${1:-}"; shift || true

case "$verb" in
genesis-root)
    genesis_root
    ;;

verify)
    unpack || { echo "cannot unpack the fixtures" >&2; exit 2; }
    [ -f "$FIXTURES/$1" ] && [ -f "$FIXTURES/$2" ] || { echo "fixture file missing" >&2; exit 2; }
    out=$("$ERIGON" snapshots verify-pbt --datadir=/erigon-hive-datadir --snapshot="$FIXTURES/$1" --preimages="$FIXTURES/$2" --block="$3" 2>&1)
    status=$?
    echo "client_exit=$status"
    case $status in
    0) exit 0 ;;
    1) echo "$out" >&2; exit 1 ;;
    *) echo "$out" >&2; exit 2 ;;
    esac
    ;;

convert)
    if [ $# -gt 1 ]; then
        echo "plain-key state: no preimage store to remove from" >&2
        exit 3
    fi
    # From the node's own datadir: `erigon init` leaves neither the aggregator
    # salt nor a commitment-state record, only a started node does. The export
    # takes its own read-only transaction.
    rm -rf /pbt/out && mkdir -p /pbt/out
    out=$("$ERIGON" --datadir /erigon-hive-datadir snapshots export-pbt --out /pbt/out 2>&1)
    status=$?
    echo "client_exit=$status"
    if [ $status -ne 0 ] || [ ! -f /pbt/out/pbt-snapshot.bin ] || [ ! -f /pbt/out/framed.bin ]; then
        echo "$out" >&2
        exit 2
    fi
    echo "snapshot=$(b64 /pbt/out/pbt-snapshot.bin)"
    echo "preimages=$(b64 /pbt/out/framed.bin)"
    ;;

*)
    echo "unknown verb: $verb" >&2
    exit 2
    ;;
esac
