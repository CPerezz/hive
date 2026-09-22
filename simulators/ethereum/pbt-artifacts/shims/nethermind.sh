#!/bin/bash
# Nethermind's side of the EIP-8347 artifact contract.
#
# Nethermind consumes the artifacts during node startup rather than through a
# subcommand, so a verb here is "boot a second, throwaway node against the
# artifacts and watch which way it goes":
#
#   * the migration step throws  -> the process exits non-zero  -> rejected
#   * the migration step passes  -> VerifyAlignment logs, startup continues
#                                -> accepted, and the node is killed
#
# Three things the client needs that hive does not give it, all synthesized
# here so that no file under clients/nethermind/ has to change:
#
#   1. A chain specification with a scheduled binaryTrieTime. PbtPlugin only
#      turns on when one is present, and PbtMigrationConfigValidator wants
#      EIP-7928 and EIP-6780 activating strictly before it. Those are added
#      *after* the genesis timestamp on purpose: a fork active at genesis
#      would change the genesis header (withdrawalsRoot, blob gas, the BAL
#      hash) and so the genesis hash, which the artifact identity is checked
#      against. Placed later, they satisfy the validator and leave the header,
#      the genesis hash and the state root exactly as the node already has them.
#   2. A manifest.json, which EIP-8347 does not define. PbtArtifactManifest
#      requires version 1 plus chainId, genesisHash, anchorHash, anchorNumber,
#      anchorMptRoot, pbtRoot, snapshotDigest, preimageDigest, formatRevision,
#      producerRevision and sourceKind. PbtImageVerifier enforces the first
#      five against the chain the node loaded; the roots and digests are
#      format-checked only, and the simulator passes the real ones.
#   3. A config file: FlatDb.Enabled with FlatLayout.Flat (the validator
#      refuses a scheduled migration without it) and Pbt.Enabled.
#
# Artifact paths are relative to the fixture tar. Optional 4th and 5th
# arguments are the artifacts' keccak digests, which the simulator computes
# because this shim has no keccak of its own.
set -u

NETHERMIND=/nethermind/nethermind
FIXTURES=/pbt/fixtures
RPC=http://127.0.0.1:8545

# Far enough out that the anchor is always pre-activation, and ordered so the
# validator's "7928 and 6780 before binaryTrieTime" rule holds.
CANCUN_TIME=1000
AMSTERDAM_TIME=2000
BINARY_TRIE_TIME=1000000000

verb="${1:-}"; shift || true

unpack() {
    [ -d "$FIXTURES" ] && return 0
    mkdir -p "$FIXTURES" || return 1
    tar -xf /pbt-fixtures.tar -C "$FIXTURES"
}

# rpc asks the node hive booted, which is the chain the artifacts must match.
rpc() {
    curl -s -X POST -H 'Content-Type: application/json' \
        --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" "$RPC" 2>/dev/null
}

await_genesis() { # -> GENESIS_HASH, STATE_ROOT, CHAIN_ID
    local i block
    for i in $(seq 1 90); do
        block=$(rpc eth_getBlockByNumber '["0x0",false]' | jq -r '.result // empty')
        if [ -n "$block" ]; then
            GENESIS_HASH=$(echo "$block" | jq -r '.hash')
            STATE_ROOT=$(echo "$block" | jq -r '.stateRoot')
            CHAIN_ID=$(rpc eth_chainId '[]' | jq -r '.result // empty')
            # The manifest carries the chain id in decimal, as ChainSpec does.
            CHAIN_ID=$((CHAIN_ID))
            return 0
        fi
        sleep 1
    done
    return 1
}

# spec writes the chain specification the throwaway node boots from.
spec() {
    jq ".config.cancunTime = $CANCUN_TIME
        | .config.amsterdamTime = $AMSTERDAM_TIME
        | .config.binaryTrieTime = $BINARY_TRIE_TIME" /genesis.json > /pbt/genesis-pbt.json
}

# manifest writes the anchor identity for $1 (snapshot digest) and $2
# (preimage digest), defaulting to the zero hash when the simulator passed
# none: both are format-checked and then discarded by the reader.
manifest() {
    local snapshot_digest="${1:-0x$(printf '0%.0s' $(seq 1 64))}"
    local preimage_digest="${2:-0x$(printf '0%.0s' $(seq 1 64))}"
    jq -n --arg chain "$CHAIN_ID" --arg genesis "$GENESIS_HASH" --arg root "$STATE_ROOT" \
        --arg pbt "${PBT_ROOT_CLAIM:-0x$(printf '0%.0s' $(seq 1 64))}" \
        --arg snap "$snapshot_digest" --arg pre "$preimage_digest" \
        '{version: 1, chainId: $chain, genesisHash: $genesis, anchorHash: $genesis,
          anchorNumber: 0, anchorMptRoot: $root, pbtRoot: $pbt,
          snapshotDigest: $snap, preimageDigest: $pre,
          formatRevision: "eip-8347", producerRevision: "hive-pbt-artifacts",
          sourceKind: "portable"}' > /pbt/manifest.json
}

# config writes the runner config. Networking and RPC stay off: the migration
# runs before InitializeNetwork, so nothing here needs them.
config() { # $1 datadir
    jq -n --arg db "$1" '{
        Init: {
            ChainSpecPath: "/pbt/genesis-pbt.json",
            BaseDbPath: $db,
            DiscoveryEnabled: false,
            ProcessingEnabled: false,
            PeerManagerEnabled: false,
            SynchronizationEnabled: false
        },
        JsonRpc: {Enabled: false},
        Network: {DiscoveryPort: 30399, P2PPort: 30399},
        FlatDb: {Enabled: true, Layout: "Flat", HistoryEnabled: false},
        Pbt: {
            Enabled: true,
            MigrationSnapshotPath: "'"$SNAPSHOT"'",
            MigrationPreimagesPath: "'"$PREIMAGES"'",
            MigrationManifestPath: "/pbt/manifest.json"
        }
    }' > /pbt/config.json
}

# boot runs the throwaway node and decides which way the migration went.
#
# Three outcomes are read off the log rather than off the exit status alone.
# Success is the line VerifyAlignment logs once the anchor is in place; the
# node is killed there, because everything after it is ordinary startup.
# Rejection is the fatal-error line: nethermind logs it the moment the
# migration step throws, then spends a while closing databases, and waiting
# out that shutdown would multiply the whole suite's runtime for no extra
# information. The exit status is still reported when the process beats us
# to it.
boot() {
    local log=/pbt/nethermind.log
    rm -f "$log"
    "$NETHERMIND" --config /pbt/config.json --log INFO >"$log" 2>&1 &
    local pid=$! i
    for i in $(seq 1 90); do
        if grep -q "EIP-8347 migration: flat state at" "$log" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            wait "$pid" 2>/dev/null
            echo "client_exit=0"
            return 0
        fi
        if grep -q "A critical error has occurred" "$log" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            wait "$pid" 2>/dev/null
            echo "client_exit=1"
            grep -a -A3 "A critical error has occurred" "$log" | head -8 >&2
            return 1
        fi
        if ! kill -0 "$pid" 2>/dev/null; then
            wait "$pid"; local status=$?
            echo "client_exit=$status"
            tail -40 "$log" >&2
            [ "$status" -eq 0 ] && return 0
            return 1
        fi
        sleep 1
    done
    kill "$pid" 2>/dev/null
    echo "the migration neither finished nor failed within 90s" >&2
    tail -40 "$log" >&2
    return 2
}

claimed_root() { echo "root=0x$(od -An -tx1 -N32 "$1" | tr -d ' \n')"; }

case "$verb" in
genesis-root)
    await_genesis || { echo "the node's RPC never answered eth_getBlockByNumber(0x0)" >&2; exit 2; }
    echo "mpt_root=$STATE_ROOT"
    exit 0
    ;;

verify|import)
    unpack || { echo "cannot unpack the fixtures" >&2; exit 2; }
    SNAPSHOT="$FIXTURES/$1"; PREIMAGES="$FIXTURES/$2"
    await_genesis || { echo "the node's RPC never answered eth_getBlockByNumber(0x0)" >&2; exit 2; }
    PBT_ROOT_CLAIM="0x$(od -An -tx1 -N32 "$SNAPSHOT" | tr -d ' \n')"

    spec || { echo "cannot build the chain specification" >&2; exit 2; }
    manifest "${4:-}" "${5:-}" || { echo "cannot write the manifest" >&2; exit 2; }
    # Verify gets a fresh database every time so a rejected artifact cannot
    # leave debris that decides the next case.
    rm -rf /pbt/nm-db
    config /pbt/nm-db || { echo "cannot write the runner config" >&2; exit 2; }

    # boot's status has to be taken straight off the call: an `if` swallows it
    # and would report every clean rejection as a crash.
    boot
    status=$?
    case "$status" in
    0)
        claimed_root "$SNAPSHOT"
        exit 0
        ;;
    1) exit 1 ;;
    *) exit 2 ;;
    esac
    ;;

convert)
    echo "nethermind exports artifacts with --Pbt.MigrationExportPath, which needs either a genesis" >&2
    echo "bootstrap or an offline preimage source as input; neither is reachable from a hive container" >&2
    echo "whose state came from genesis.json alone, so the produce leg is not wired" >&2
    exit 3
    ;;

*)
    echo "unknown verb: $verb" >&2
    exit 2
    ;;
esac
