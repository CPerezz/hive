# pbt-artifacts

Conformance for the [EIP-8347](https://eips.ethereum.org/EIPS/eip-8347) offline
migration artifacts: the **preimage file** and the **PBT snapshot**.

Every client is handed the same byte-canonical pair and must accept the sound
one, reject each unsound one, and where it can convert, reproduce both
files byte for byte from the anchor state. Clients that lack a feature report
it as unsupported, so the run doubles as a capability matrix.

Scope is the offline artifacts only. The live migration (BAL replay, shadow
roots, the fork switch) belongs to
[pbt-devnet](https://github.com/CPerezz/pbt-devnet).

## Running

```bash
./hive --sim ethereum/pbt-artifacts \
       --client-file simulators/ethereum/pbt-artifacts/clients.yaml

go run ./simulators/ethereum/pbt-artifacts/tools/matrix workspace/logs > CAPABILITY.md
```

`clients.yaml` pins each client to the branch carrying its PBT work, since
none of it is on a default branch. To measure a local build instead, point the
stock client Dockerfile at your own image:

```yaml
- client: go-ethereum
  nametag: local
  build_args: {baseimage: my-geth, tag: dev}
```

## How a client is driven

One shim per client, uploaded into the container at
`/hive-bin/pbt-artifacts.sh` and invoked over hive's exec channel. **No file
under `clients/` is touched**, so adding a client is one new script in
`shims/`, named after the client (`shims/go-ethereum.sh`). A client with no
shim gets `shims/unsupported.sh` and shows up as a gap rather than a failure.

A shim may need more than a command line. Nethermind consumes the artifacts
during node startup rather than through a subcommand, so `shims/nethermind.sh`
boots a throwaway node against them and reads the outcome off its log, and it
synthesizes the two inputs hive does not provide: a chainspec carrying a
scheduled `binaryTrieTime` (with the EIP-6780 and EIP-7928 activations the
validator demands, placed after the genesis timestamp so the genesis header
and hash stay exactly as the node already had them) and the `manifest.json`
that EIP-8347 does not define. Still no client file changed.

The shim is the entire coupling surface:

| verb | arguments | stdout | meaning |
|---|---|---|---|
| `genesis-root` | — | `mpt_root=0x…` | the anchor state the client built |
| `verify` | `<snapshot> <preimages> <anchor> [snapshotDigest] [preimageDigest]` | `root=0x…` | check the pair, write nothing |
| `convert` | `<anchor>` | `root=0x…`, `snapshot=<b64>`, `preimages=<b64>` | produce what it can |
| `import` | `<snapshot> <preimages> <anchor>` | `root=0x…` | check and persist |

The two trailing digest arguments are the artifacts' keccak hashes, computed
by the simulator because a shell has no keccak. A client whose consumption
path wants them, and nethermind wants both inside its manifest, would
otherwise be handed something arbitrary and reject the digest instead of the
clause the case is about.

Exit `0` accepted, `1` rejected cleanly, `3` unsupported, anything else is a
crash. A crash is never scored as a rejection, and a shim must not wrap the
client in `|| exit 1`: it echoes the real status as `client_exit=`.

A client may produce one artifact and not the other, which erigon does: it
writes the preimage file and has no portable snapshot. `convert` prints only
the lines it can, and a missing one is read as that artifact being
unsupported rather than as a failure. A `root=` is asked only of a client
that produced a snapshot.

Artifact paths are relative to the fixture tar, which the simulator uploads
once as `/pbt-fixtures.tar`; the shim untars it on first use. The anchor is
block 0, so no chain import is needed.

## Scoring

- `genesis/state-root` gates everything. A client whose block-0 state root is
  not the one the fixtures were derived from has its remaining cases reported
  `inconclusive`, never failed.
- `<suite>/valid` gates that suite. If the client rejects the sound pair, its
  rejections prove nothing and the rest are `inconclusive`; if it answers
  unsupported, the suite collapses to a single `NOT-RUN` row rather than one
  failure per fixture.
- Reject cases pass only on exit `1` **with** something on stderr.
- Cases marked `unspecified` in the manifest are run and reported but never
  scored: the EIP does not settle them (see below).
- `capability-matrix` always passes and carries the client's row in its log.
- `agreement/<artifact>` is the run's own verdict rather than any client's.
  Byte-canonicality is a claim about producers as a set, so every producer of
  an artifact has to emit the same bytes as every other and as the canonical
  fixture. One producer diverging fails. No producer matching fails hardest,
  because then either every implementation is wrong or the fixture is. A
  single producer, however correct, leaves agreement unproven and reports
  inconclusive.

## Fixtures

`fixtures/genesis.json` is the anchor state, one account per embedding rule:
code-size boundaries around the 31-byte chunk, a PUSH32 straddling a chunk
boundary, a zero chunk that is a PUSHDATA continuation and one that is not,
code spanning two code groups, shared bytecode, EIP-7702 delegations to shared
and distinct targets, code that merely starts with `0xef0100`, storage either
side of the header/storage-zone split and across several groups, maximum nonce
and balance.

`fixtures/manifest.json` names every case, the clause it breaks and the
outcome expected. 37 cases must be rejected; 4 are `unspecified`:

| case | why it is open |
|---|---|
| `snapshot/empty` | `leafCount` 0: the reference converter refuses it, the EIP is silent |
| `snapshot/reserved-header-subindex` | sub-indices 3–63, the gap between the delegation leaf and storage |
| `snapshot/codeless-account-without-code-hash` | a verifier may infer the empty-code hash instead of requiring the leaf |
| `preimages/storage-less-record-absent` | whether a storage-less account needs a `slotCount == 0` record |

Regenerating needs a go-ethereum checkout of the PBT fork beside `hive/`.
The generator ships with its module file disabled, because hive's simulator
build walks every `go.mod` under `simulators/` and CI has no such checkout,
so put it in place first:

```bash
cd fixtures/gen
cp go.mod.dist go.mod && cp go.sum.dist go.sum
go run . -geth /path/to/geth -out ..
cd .. && ./validate.sh /path/to/geth     # the same judgement, without docker
```

`validate.sh` is how to check the fixture set, or one binary, without a hive
run at all.

## Provenance of the fixture bytes

The preimage file is derived from the allocation, not taken from a client:
the EIP fixes the framing and the order, and the allocation names every
address and slot key, so `fixtures/gen` computes it and holds geth's
converter to it. Nothing about those bytes depends on an implementation
being measured against them.

The snapshot needs a tree, so its bytes come from go-ethereum's converter,
whose PBT roots are pinned against the `eip8297_vectors.json` state vectors
exported from execution-specs. `agreement/snapshot` is what turns that into
a cross-client claim, and it stays inconclusive until a second client can
produce one.

Measured today: erigon and geth emit byte-identical preimage files, and geth
is the only snapshot producer.
