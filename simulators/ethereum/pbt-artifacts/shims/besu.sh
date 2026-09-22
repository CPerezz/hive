#!/bin/bash
# Besu's side of the EIP-8347 artifact contract.
#
# Besu migrates by resolving the trie type per header while it processes
# blocks (StateRootCommitterFactory.isBinaryTrie), so it has no offline
# converter and no artifact importer: there is nothing to call. Both verbs
# report unsupported until besu grows a subcommand that reads or writes the
# EIP-8347 files.
set -u

verb="${1:-}"; shift || true

case "$verb" in
genesis-root)
    for _ in $(seq 1 90); do
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

verify|import|convert)
    echo "besu migrates per header inside block processing and exposes no offline artifact command" >&2
    exit 3
    ;;

*)
    echo "unknown verb: $verb" >&2
    exit 2
    ;;
esac
