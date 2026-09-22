#!/bin/bash
# Erigon's side of the EIP-8347 artifact contract.
#
# Erigon is a producer, not a consumer. `snapshots export-preimages` writes
# the EIP's preimage file (fixed width records ordered by keccak256 of the
# plain key, plus a preimages.meta.json pin) and verifies its own state root
# against the canonical header before writing. It is present on stock
# main-latest, so this needs no branch pin.
#
# What it has no command for:
#   * consuming an externally produced snapshot or preimage file. There is no
#     importer, so verify and import are unsupported.
#   * producing a portable snapshot. `integration commitment rebuild` rewrites
#     erigon's own commitment domain in place, as .kv files plus an
#     erigondb.toml, which is not the EIP's artifact.
#
# So convert emits preimages and stays silent about the snapshot, which the
# simulator reads as "this client produces one of the two".
set -u

ERIGON=/usr/local/bin/erigon
DATADIR=/erigon-hive-datadir
OUT=/pbt/out
RPC=http://127.0.0.1:8545

verb="${1:-}"; shift || true

case "$verb" in
genesis-root)
    for _ in $(seq 1 90); do
        root=$(curl -s -X POST -H 'Content-Type: application/json' \
            --data '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",false]}' \
            "$RPC" 2>/dev/null | jq -r '.result.stateRoot // empty')
        if [ -n "$root" ]; then
            echo "mpt_root=$root"
            exit 0
        fi
        sleep 1
    done
    echo "the node's RPC never answered eth_getBlockByNumber(0x0)" >&2
    exit 2
    ;;

verify|import)
    echo "erigon has no importer for an externally produced snapshot or preimage file" >&2
    exit 3
    ;;

convert)
    # Read from the node's own datadir rather than a throwaway one. The export
    # reads the latest commitment state, and `erigon init` leaves neither that
    # record nor the aggregator salt behind: only a node that has actually
    # started writes them. Doing this while the node runs is safe, the export
    # takes its own read-only transaction.
    rm -rf "$OUT"
    mkdir -p "$OUT"
    out=$("$ERIGON" --datadir "$DATADIR" snapshots export-preimages --out "$OUT" 2>&1)
    status=$?
    echo "client_exit=$status"
    if [ $status -ne 0 ]; then
        echo "$out" >&2
        exit 1
    fi
    if [ ! -f "$OUT/framed.bin" ]; then
        echo "export-preimages exited 0 but wrote no framed.bin" >&2
        echo "$out" >&2
        exit 2
    fi
    echo "preimages=$(base64 -w0 "$OUT/framed.bin" 2>/dev/null || base64 "$OUT/framed.bin" | tr -d '\n')"
    echo "no portable snapshot: 'integration commitment rebuild' converts erigon's own commitment domain in place" >&2
    exit 0
    ;;

*)
    echo "unknown verb: $verb" >&2
    exit 2
    ;;
esac
