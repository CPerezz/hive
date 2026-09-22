# pbt-artifacts

Conformance for the [EIP-8347](https://eips.ethereum.org/EIPS/eip-8347) offline
migration artifacts, the preimage file and the PBT snapshot. Every client is
handed the same byte-canonical pair and must accept the sound one, reject
each unsound one for the reason its clause names, and where it can produce
an artifact, emit the same bytes. Clients that lack a feature report it as
unsupported, so the run doubles as a capability matrix.

The live migration (BAL replay, shadow roots, the fork switch) belongs to
[pbt-devnet](https://github.com/CPerezz/pbt-devnet).

## Running

```bash
./hive --sim ethereum/pbt-artifacts \
       --client-file simulators/ethereum/pbt-artifacts/clients.yaml

go run ./simulators/ethereum/pbt-artifacts/tools/matrix workspace/logs > CAPABILITY.md
```

`clients.yaml` pins each client to the branch carrying its PBT work. To
measure a local build, point the stock client Dockerfile at your image:

```yaml
- client: go-ethereum
  nametag: local
  build_args: {baseimage: my-geth, tag: dev}
```

## Driving a client

One shim per client, uploaded to `/hive-bin/pbt-artifacts.sh` and invoked
over hive's exec channel, with `shims/common.sh` beside it. Nothing under
`clients/` changes; adding a client is one script in `shims/`, named after
the client. A client with no shim gets `shims/unsupported.sh` and shows up as
a gap, not a failure.

| verb | arguments | stdout |
|---|---|---|
| `genesis-root` | | `mpt_root=0x…` |
| `verify` | `<snapshot> <preimages> <anchor> [snapshotDigest] [preimageDigest]` | |
| `convert` | `<anchor>` | `snapshot=<b64>` and/or `preimages=<b64>` |

Exit `0` accepted, `1` rejected, `3` unsupported, anything else a crash. A
crash never counts as a rejection, and a shim must not wrap the client in
`|| exit 1`: it echoes the real status as `client_exit=`. Paths are relative
to the fixture tar the simulator uploads; the anchor is block 0. The digests
are passed because a shell has no keccak and some clients want them.
`convert` prints only what the client can produce: a missing line means
that artifact is unsupported.

A shim may need more than a command line. Nethermind consumes artifacts
during node startup, so its shim boots a throwaway node and reads the
outcome off the log, synthesizing a chainspec with a scheduled
`binaryTrieTime` (fork times placed after genesis, so the genesis hash is
unchanged) and the `manifest.json` the EIP does not define.

Optionally, `shims/<client>.reasons.json` maps case ids to a regexp over the
client's stderr. With it, a rejection counts only when it names the clause
the case breaks; without it, rejections are counted but not attributed.

## Scoring

- `genesis/state-root` gates everything: a mismatch makes the rest
  `inconclusive`.
- `<suite>/valid` gates its suite: reject the sound pair and the rest is
  `inconclusive`; answer unsupported and the suite becomes one `NOT-RUN` row.
- A reject case passes on exit `1` with something on stderr, and with
  reasons present, only for the attributed reason.
- `unspecified` cases are run and reported, never scored.
- `agreement/<artifact>` is the run's verdict, not a client's: every
  producer must match the canonical bytes. One diverging fails; none matching
  fails hardest; a single producer is reported inconclusive.

## Fixtures

`fixtures/genesis.json`: one account per embedding rule. Code sizes around
the 31-byte chunk, PUSH32 and PUSH2 straddling a chunk, PUSHDATA running
past the code's end (leading count at the 31 cap), a zero chunk that must be
absent and a trailing one, code entirely zero, code spanning two code
groups, shared bytecode, code that merely starts with `0xef0100` at 23 and
24 bytes, delegations to a shared target, a distinct one, a missing account,
an EOA and another delegation, storage either side of the header split and
across groups, an account with only overflow storage, a contract with zero
balance and nonce, maximum nonce and balance. The 24 KiB maximum is left
out: 793 leaves for nothing the two-group case does not cover. The produce
leg is measured on this sound state only.

`fixtures/manifest.json` names every case, its clause and expected outcome.
One case is `unspecified`: `snapshot/empty`, since whether a zero-account
state is convertible at all is not settled. Every other question is
answered by EIP-8297's embedding rules and is scored.

The preimage file is derived from the allocation, not taken from a client.
The snapshot needs a tree, so its bytes come from the reference converter,
but its leaf set is checked against an independent derivation of the
embedding rules (values, chunking, presence) before anything is written, and
`agreement/snapshot` is what turns that into a cross-client claim.

Regenerating needs the go-ethereum PBT fork checked out beside `hive/`. The
generator ships with its module files disabled so hive's simulator build
never sees them:

```bash
cd fixtures/gen && cp go.mod.dist go.mod && cp go.sum.dist go.sum
go run . -geth /path/to/geth -out ..
cd .. && ./validate.sh /path/to/geth     # the same judgement, no docker
```
