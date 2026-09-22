#!/bin/bash
# Nethermind consumes the artifacts during node startup, so verify boots a
# throwaway node against them and reads the outcome off its log. Written
# against NethermindEth/nethermind@pbt-state (f56fb98e); see README.md for
# the chainspec and manifest it synthesizes.
set -u
. /hive-bin/pbt-common.sh

NETHERMIND=/nethermind/nethermind
ZERO=0x$(printf '%064d' 0)
verb="${1:-}"; shift || true

# All three after the genesis timestamp, so the genesis header is unchanged
# and the validator's "7928 and 6780 before binaryTrieTime" holds.
spec() {
    jq '.config.cancunTime = 1000 | .config.amsterdamTime = 2000 | .config.binaryTrieTime = 1000000000' \
        /genesis.json > /pbt/genesis-pbt.json
}

manifest() { # snapshot digest, preimage digest
    local block chain
    block=$(genesis_block) || return 1
    chain=$(curl -sf -X POST -H 'Content-Type: application/json' \
        --data '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}' http://127.0.0.1:8545 | jq -r '.result')
    jq -n --arg chain "$((chain))" --arg genesis "$(echo "$block" | jq -r .hash)" \
        --arg root "$(echo "$block" | jq -r .stateRoot)" --arg pbt "$PBT_ROOT" \
        --arg snap "${1:-$ZERO}" --arg pre "${2:-$ZERO}" \
        '{version: 1, chainId: $chain, genesisHash: $genesis, anchorHash: $genesis, anchorNumber: 0,
          anchorMptRoot: $root, pbtRoot: $pbt, snapshotDigest: $snap, preimageDigest: $pre,
          formatRevision: "eip-8347", producerRevision: "hive-pbt-artifacts", sourceKind: "portable"}' \
        > /pbt/manifest.json
}

config() { # snapshot path, preimages path
    jq -n --arg snap "$1" --arg pre "$2" '{
        Init: {ChainSpecPath: "/pbt/genesis-pbt.json", BaseDbPath: "/pbt/nm-db", DiscoveryEnabled: false,
               ProcessingEnabled: false, PeerManagerEnabled: false, SynchronizationEnabled: false},
        JsonRpc: {Enabled: false},
        Network: {DiscoveryPort: 30399, P2PPort: 30399},
        FlatDb: {Enabled: true, Layout: "Flat", HistoryEnabled: false},
        Pbt: {Enabled: true, MigrationSnapshotPath: $snap, MigrationPreimagesPath: $pre,
              MigrationManifestPath: "/pbt/manifest.json"}
    }' > /pbt/config.json
}

# boot runs the throwaway node. The import step throws InvalidDataException
# on a bad artifact; any other fatal is the shim's own fault or the machine's,
# and is a crash. Success is the line VerifyAlignment logs, after which the
# node is killed rather than left to start networking.
boot() {
    local log=/pbt/nethermind.log
    rm -rf "$log" /pbt/nm-db
    "$NETHERMIND" --config /pbt/config.json --log INFO >"$log" 2>&1 &
    local pid=$!
    for _ in $(seq 1 90); do
        if grep -q "EIP-8347 migration: flat state at" "$log" 2>/dev/null; then
            kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
            echo "client_exit=0"
            return 0
        fi
        if grep -q "A critical error has occurred" "$log" 2>/dev/null; then
            kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
            grep -a -A3 "A critical error has occurred" "$log" | head -8 >&2
            if grep -q "InvalidDataException" "$log"; then
                echo "client_exit=1"
                return 1
            fi
            echo "client_exit=2"
            return 2
        fi
        if ! kill -0 "$pid" 2>/dev/null; then
            wait "$pid"; local status=$?
            echo "client_exit=$status"
            tail -20 "$log" >&2
            return $status
        fi
        sleep 1
    done
    kill "$pid" 2>/dev/null
    echo "neither finished nor failed within 90s" >&2
    return 2
}

case "$verb" in
genesis-root)
    genesis_root
    ;;

verify)
    unpack || { echo "cannot unpack the fixtures" >&2; exit 2; }
    PBT_ROOT="0x$(od -An -tx1 -N32 "$FIXTURES/$1" | tr -d ' \n')"
    spec && manifest "${4:-}" "${5:-}" && config "$FIXTURES/$1" "$FIXTURES/$2" || { echo "cannot write the node's inputs" >&2; exit 2; }
    boot
    ;;

convert)
    echo "nethermind exports with --Pbt.MigrationExportPath, which needs a genesis bootstrap or an" >&2
    echo "offline preimage source as input; a container booted from genesis.json alone has neither" >&2
    exit 3
    ;;

*)
    echo "unknown verb: $verb" >&2
    exit 2
    ;;
esac
