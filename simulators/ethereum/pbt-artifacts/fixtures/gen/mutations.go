package main

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie/bintrie"
)

// mutations is the catalogue, one entry per way an artifact can lie. A leaf
// mutation recomputes the claimed root unless keepRoot is set, so it reaches
// past check 1 to the rule it targets.
func mutations(valid *artifacts) []mutation {
	var (
		zero      [32]byte
		one       = [32]byte{31: 1}
		emptyCode = [32]byte(crypto.Keccak256(nil))
		codeHash  = crypto.Keccak256Hash(fill(chunk))
		reject    = func(m mutation) mutation { m.expect = "reject"; return m }
	)
	dropRecord := func(addr common.Address) func([]record) []record {
		return func(recs []record) []record { return deleteRecord(recs, addr) }
	}
	preimages := func(id, clause, note string, f func([]record) []record) mutation {
		return reject(mutation{id: "preimages/" + id, suite: "preimages", clause: clause, note: note, records: f})
	}
	rawPre := func(id, clause, note string, f func([]byte) []byte) mutation {
		return reject(mutation{id: "preimages/" + id, suite: "preimages", clause: clause, note: note, rawPre: f})
	}
	snapshot := func(id, clause, note string, f func([]leaf) []leaf) mutation {
		return reject(mutation{id: "snapshot/" + id, suite: "snapshot", clause: clause, note: note, leaves: f})
	}
	rawSnap := func(id, clause, note string, f func([]byte) []byte) mutation {
		return reject(mutation{id: "snapshot/" + id, suite: "snapshot", clause: clause, note: note, rawSnap: f})
	}
	keep := func(m mutation) mutation { m.keepRoot = true; return m }
	verbatim := func(m mutation) mutation { m.verbatim = true; return m }
	withRecords := func(m mutation, f func([]record) []record) mutation { m.records = f; return m }

	return []mutation{
		// The preimage file.
		rawPre("trailing-byte", "preimage.no-trailing-bytes", "",
			func(b []byte) []byte { return append(bytes.Clone(b), 0x00) }),
		rawPre("truncated-record", "preimage.record-self-delimiting", "the last slot key is cut short",
			func(b []byte) []byte { return bytes.Clone(b)[:len(b)-1] }),
		rawPre("slot-count-huge", "preimage.record-self-delimiting", "the first record claims 2^32-1 slots",
			func(b []byte) []byte {
				out := bytes.Clone(b)
				binary.BigEndian.PutUint32(out[common.AddressLength:], 0xffffffff)
				return out
			}),
		verbatim(preimages("raw-address-order", "preimage.hashed-key-order", "sorted by raw address",
			func(recs []record) []record {
				slices.SortStableFunc(recs, func(x, y record) int { return bytes.Compare(x.addr[:], y.addr[:]) })
				return recs
			})),
		verbatim(preimages("raw-slot-order", "preimage.hashed-key-order", "slots sorted by number",
			func(recs []record) []record {
				for i := range recs {
					slices.SortStableFunc(recs[i].slots, func(x, y common.Hash) int { return bytes.Compare(x[:], y[:]) })
				}
				return recs
			})),
		verbatim(preimages("duplicate-address", "preimage.address-appears-once", "one account twice, adjacent",
			func(recs []record) []record { return slices.Insert(recs, 1, recs[0]) })),
		verbatim(preimages("duplicate-slot", "preimage.no-duplicate-slots", "one slot twice, adjacent",
			func(recs []record) []record {
				i := findRecord(recs, storageSpread)
				recs[i].slots = slices.Insert(recs[i].slots, 1, recs[i].slots[0])
				return recs
			})),
		preimages("missing-account", "converter.preimage-set-matches-leaves", "", dropRecord(eoaBalance)),
		preimages("surplus-account", "converter.preimage-set-matches-leaves", "",
			func(recs []record) []record {
				return append(recs, record{addr: common.HexToAddress("0x00000000000000000000000000000000deadbeef")})
			}),
		preimages("missing-header-slot", "converter.preimage-set-matches-leaves", "slot 63 of storageSpread",
			func(recs []record) []record {
				i := findRecord(recs, storageSpread)
				recs[i].slots = slices.DeleteFunc(recs[i].slots, func(s common.Hash) bool { return s == h(63) })
				return recs
			}),
		preimages("surplus-header-slot", "converter.preimage-set-matches-leaves", "slot 7 of storageSpread",
			func(recs []record) []record {
				i := findRecord(recs, storageSpread)
				recs[i].slots = append(recs[i].slots, h(7))
				return recs
			}),
		preimages("missing-overflow-slot", "converter.preimage-set-matches-leaves",
			"slot 256 of storageSpread; its stem is a one-way hash, so the leaf can never be keyed",
			func(recs []record) []record {
				i := findRecord(recs, storageSpread)
				recs[i].slots = slices.DeleteFunc(recs[i].slots, func(s common.Hash) bool { return s == h(256) })
				return recs
			}),
		rawPre("empty-file", "converter.preimage-set-matches-leaves", "", func([]byte) []byte { return []byte{} }),

		// The snapshot: framing.
		rawSnap("wrong-claimed-root", "snapshot.root-recomputed", "",
			func(b []byte) []byte { out := bytes.Clone(b); out[31] ^= 1; return out }),
		rawSnap("leaf-count-high", "snapshot.leaf-count-recomputed", "",
			func(b []byte) []byte { return withCount(b, uint64(len(valid.leaves))+1) }),
		rawSnap("leaf-count-low", "snapshot.leaf-count-recomputed", "",
			func(b []byte) []byte { return withCount(b, uint64(len(valid.leaves))-1) }),
		rawSnap("leaf-count-huge", "snapshot.leaf-count-recomputed", "2^62 leaves claimed over the valid body",
			func(b []byte) []byte { return withCount(b, 1<<62) }),
		rawSnap("truncated-stream", "snapshot.record-self-delimiting", "",
			func(b []byte) []byte { return bytes.Clone(b)[:len(b)-1] }),
		rawSnap("trailing-garbage", "snapshot.leaf-count-recomputed", "",
			func(b []byte) []byte { return append(bytes.Clone(b), 0xde, 0xad) }),
		keep(snapshot("records-out-of-order", "snapshot.ascending-key-order", "eoaBalance's first two leaves swapped",
			func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				leaves[i], leaves[i+1] = leaves[i+1], leaves[i]
				return leaves
			})),
		keep(snapshot("duplicate-key", "snapshot.ascending-key-order", "",
			func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				return slices.Insert(leaves, i+1, leaves[i])
			})),
		keep(snapshot("reserved-zone", "snapshot.zone-byte", "eoaBalance's basic-data key in zone 0x02",
			func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				leaves[i].key = bytes.Clone(leaves[i].key)
				leaves[i].key[0] = 0x02
				return leaves
			})),
		keep(snapshot("wrong-key-length", "snapshot.zone-fixes-key-length", "an account-zone key at storage-zone length",
			func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				leaves[i].key = append(bytes.Clone(leaves[i].key), make([]byte, bintrie.StorageKeyLength-bintrie.AccountKeyLength)...)
				return leaves
			})),
		rawSnap("non-canonical-value", "snapshot.canonical-integer-value", "a leading zero byte on the first value",
			func(b []byte) []byte {
				return reencodeFirst(b, func(v []byte) []byte { return append([]byte{0}, v...) })
			}),
		rawSnap("value-too-long", "snapshot.canonical-integer-value", "a 33-byte value",
			func(b []byte) []byte {
				return reencodeFirst(b, func([]byte) []byte { return bytes.Repeat([]byte{1}, 33) })
			}),
		rawSnap("record-not-a-pair", "snapshot.record-is-key-value-pair", "a one-item list",
			func(b []byte) []byte { return reencodeFirstRaw(b, func(k, _ []byte) []byte { return mustRLP1(k) }) }),
		rawSnap("non-canonical-rlp-length", "snapshot.record-is-key-value-pair", "a long-form length prefix on a short string",
			func(b []byte) []byte {
				return reencodeFirstRaw(b, func(k, v []byte) []byte { return longFormPair(k, v) })
			}),

		// The snapshot: what the leaves say about the state.
		keep(snapshot("zero-value-present", "snapshot.no-zero-values",
			"slot 5 of storageSpread held as 32 zero bytes; no tree can commit to it, so the valid root stays",
			func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{key: bintrie.HeaderKey(storageSpread, bintrie.HeaderStorageOffset+5), value: zero})
			})),
		snapshot("flipped-value", "verification.consensus-anchoring", "eoaBalance's balance, root recomputed",
			func(leaves []leaf) []leaf {
				leaves[findKey(leaves, bintrie.BasicDataKey(eoaBalance))].value[31] ^= 1
				return leaves
			}),
		snapshot("nonzero-version", "embedding.version-zero", "",
			func(leaves []leaf) []leaf {
				leaves[findKey(leaves, bintrie.BasicDataKey(eoaBalance))].value[0] = 1
				return leaves
			}),
		snapshot("basic-data-reserved-garbage", "embedding.basic-data-layout", "",
			func(leaves []leaf) []leaf {
				v := &leaves[findKey(leaves, bintrie.BasicDataKey(eoaBalance))].value
				v[1], v[2], v[3] = 0xff, 0xff, 0xff
				return leaves
			}),
		snapshot("wrong-code-size", "verification.code-limb", "codeChunkExact claims 7 bytes",
			func(leaves []leaf) []leaf {
				binary.BigEndian.PutUint32(leaves[findKey(leaves, bintrie.BasicDataKey(codeChunkExact))].value[4:8], 7)
				return leaves
			}),
		snapshot("codeless-nonzero-code-size", "verification.code-limb", "eoaBalance claims 1 byte of code with no chunks",
			func(leaves []leaf) []leaf {
				binary.BigEndian.PutUint32(leaves[findKey(leaves, bintrie.BasicDataKey(eoaBalance))].value[4:8], 1)
				return leaves
			}),
		snapshot("codeless-wrong-code-hash", "verification.consensus-anchoring", "eoaBalance's code hash is not keccak(empty)",
			func(leaves []leaf) []leaf {
				leaves[findKey(leaves, bintrie.CodeHashKey(eoaBalance))].value = one
				return leaves
			}),
		snapshot("missing-code-chunk", "verification.code-limb", "chunk 0 of the 31-byte JUMPDEST code",
			func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.CodeChunkKey(codeHash, 0))
				return slices.Delete(leaves, i, i+1)
			}),
		snapshot("flipped-pushdata-byte", "verification.code-limb", "chunk 0 of the 31-byte JUMPDEST code, count 0 to 1",
			func(leaves []leaf) []leaf {
				leaves[findKey(leaves, bintrie.CodeChunkKey(codeHash, 0))].value[0] = 1
				return leaves
			}),
		snapshot("pushdata-byte-out-of-range", "embedding.code-chunk-layout", "a leading count of 0xff",
			func(leaves []leaf) []leaf {
				leaves[findKey(leaves, bintrie.CodeChunkKey(codeHash, 0))].value[0] = 0xff
				return leaves
			}),
		snapshot("orphan-code-leaves", "verification.code-limb", "chunks under a code hash no account holds",
			func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{key: bintrie.CodeChunkKey(crypto.Keccak256Hash([]byte("orphan")), 0), value: one})
			}),
		snapshot("surplus-account-leaves", "converter.preimage-set-matches-leaves", "an account with no preimage record",
			func(leaves []leaf) []leaf {
				addr := common.HexToAddress("0x00000000000000000000000000000000deadbeef")
				leaves = insertSorted(leaves, leaf{key: bintrie.BasicDataKey(addr), value: basicData(0, 0, big.NewInt(1))})
				return insertSorted(leaves, leaf{key: bintrie.CodeHashKey(addr), value: emptyCode})
			}),
		snapshot("delegation-and-code-hash", "embedding.one-of-codehash-or-delegation", "delegatedA holds both",
			func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{key: bintrie.CodeHashKey(delegatedA), value: emptyCode})
			}),
		snapshot("delegation-leaf-missing", "embedding.one-of-codehash-or-delegation", "delegatedA holds neither",
			func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.DelegationKey(delegatedA))
				return slices.Delete(leaves, i, i+1)
			}),
		snapshot("codeless-without-code-hash", "embedding.one-of-codehash-or-delegation", "eoaBalance holds neither",
			func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.CodeHashKey(eoaBalance))
				return slices.Delete(leaves, i, i+1)
			}),
		snapshot("delegation-with-code-leaves", "embedding.delegation-has-no-code-leaves",
			"chunks under keccak(indicator); the MPT code hash of delegatedA is that hash, so a code-hash-to-chunks mapping would accept them",
			func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{key: bintrie.CodeChunkKey(crypto.Keccak256Hash(delegation(delegateTarget)), 0), value: one})
			}),
		snapshot("delegation-wrong-designator", "embedding.delegation-leaf-layout", "0xef0200 prefix",
			func(leaves []leaf) []leaf {
				leaves[findKey(leaves, bintrie.DelegationKey(delegatedA))].value[1] = 0x02
				return leaves
			}),
		snapshot("delegation-wrong-code-size", "embedding.delegation-code-size-23", "24",
			func(leaves []leaf) []leaf {
				binary.BigEndian.PutUint32(leaves[findKey(leaves, bintrie.BasicDataKey(delegatedA))].value[4:8], 24)
				return leaves
			}),
		snapshot("delegation-code-size-zero", "embedding.delegation-code-size-23", "0",
			func(leaves []leaf) []leaf {
				binary.BigEndian.PutUint32(leaves[findKey(leaves, bintrie.BasicDataKey(delegatedA))].value[4:8], 0)
				return leaves
			}),
		snapshot("delegation-padding-garbage", "embedding.delegation-leaf-layout", "",
			func(leaves []leaf) []leaf {
				leaves[findKey(leaves, bintrie.DelegationKey(delegatedA))].value[31] = 0xff
				return leaves
			}),
		snapshot("reserved-header-subindex", "converter.preimage-set-matches-leaves",
			"eoaBalance sub-index 3: no key the EIP defines resolves there, so no preimage can name it",
			func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{key: bintrie.HeaderKey(eoaBalance, 3), value: one})
			}),
		snapshot("header-slot-in-storage-zone", "embedding.header-holds-slots-0-63", "storageSpread's slot 0 keyed as overflow",
			func(leaves []leaf) []leaf {
				overflow := bytes.Clone(leaves[findKey(leaves, bintrie.StorageSlotKey(storageSpread, h(64).Bytes()))].key)
				overflow[len(overflow)-1] = 0
				i := findKey(leaves, bintrie.HeaderKey(storageSpread, bintrie.HeaderStorageOffset))
				moved := leaf{key: overflow, value: leaves[i].value}
				return insertSorted(slices.Delete(leaves, i, i+1), moved)
			}),
		snapshot("storage-leaf-unkeyable", "embedding.storage-key-derivation", "storageSpread's slot 512 keyed under tree_index 0",
			func(leaves []leaf) []leaf {
				group0 := bytes.Clone(leaves[findKey(leaves, bintrie.StorageSlotKey(storageSpread, h(64).Bytes()))].key)
				group0[len(group0)-1] = 200
				i := findKey(leaves, bintrie.StorageSlotKey(storageSpread, h(512).Bytes()))
				moved := leaf{key: group0, value: leaves[i].value}
				return insertSorted(slices.Delete(leaves, i, i+1), moved)
			}),
		withRecords(snapshot("anchored-elsewhere", "verification.consensus-anchoring",
			"eoaBalance removed from both files: internally consistent, but not the anchor state",
			func(leaves []leaf) []leaf {
				for _, key := range [][]byte{bintrie.BasicDataKey(eoaBalance), bintrie.CodeHashKey(eoaBalance)} {
					i := findKey(leaves, key)
					leaves = slices.Delete(leaves, i, i+1)
				}
				return leaves
			}),
			dropRecord(eoaBalance)),

		// Open in the EIP: run and reported, never scored.
		{
			id: "snapshot/empty", suite: "snapshot", clause: "unspecified.empty-snapshot", expect: "unspecified",
			note:    "leafCount 0 under the empty-tree root",
			rawSnap: func([]byte) []byte { return make([]byte, snapshotHeaderSize) },
		},
	}
}

func deleteRecord(recs []record, addr common.Address) []record {
	return slices.DeleteFunc(recs, func(r record) bool { return r.addr == addr })
}

func withCount(b []byte, count uint64) []byte {
	out := bytes.Clone(b)
	binary.BigEndian.PutUint64(out[32:snapshotHeaderSize], count)
	return out
}

// reencodeFirst rewrites the first record with a value the writers cannot
// emit, leaving every other record and the header as they were.
func reencodeFirst(b []byte, value func([]byte) []byte) []byte {
	return reencodeFirstRaw(b, func(k, v []byte) []byte { return mustRLP(k, value(v)) })
}

// reencodeFirstRaw rewrites the first record's bytes wholesale.
func reencodeFirstRaw(b []byte, rec func(key, value []byte) []byte) []byte {
	root, leaves, err := decodeSnapshot(b)
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	buf.Write(root[:])
	buf.Write(binary.BigEndian.AppendUint64(nil, uint64(len(leaves))))
	for i, l := range leaves {
		if i == 0 {
			buf.Write(rec(l.key, common.TrimLeftZeroes(l.value[:])))
			continue
		}
		buf.Write(encodeLeaf(l))
	}
	return buf.Bytes()
}
