#!/usr/bin/env sh
# The shim for a client that has no EIP-8347 artifact support, and for any
# client this simulator has never heard of. Every verb is unsupported, which
# the matrix shows as a gap rather than as a failure.
echo "this client has no EIP-8347 offline artifact support in hive: add simulators/ethereum/pbt-artifacts/shims/<client>.sh" >&2
exit 3
