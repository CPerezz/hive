#!/bin/bash
# Besu resolves the trie type per header inside block processing and has no
# offline artifact command in either direction.
set -u
. /hive-bin/pbt-common.sh

verb="${1:-}"; shift || true

case "$verb" in
genesis-root)
    genesis_root
    ;;

verify|convert)
    echo "besu has no offline artifact command" >&2
    exit 3
    ;;

*)
    echo "unknown verb: $verb" >&2
    exit 2
    ;;
esac
