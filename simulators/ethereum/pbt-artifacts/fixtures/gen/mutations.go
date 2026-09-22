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

// mutations is the catalogue: one entry per way an artifact can lie, plus the
// cases whose treatment the EIP leaves open, which are carried as
// "unspecified" so they are reported without being scored.
//
// A case that recomputes the claimed root (the default for the leaf-set
// mutations) reaches past the internal-consistency check to the layer it
// targets, which is the consensus-anchoring one. A case marked keepRoot
// leaves the valid root in place and so is caught by the first check.
func mutations(valid *artifacts) []mutation {
	var (
		zeroValue [32]byte
		oneValue  = [32]byte{31: 1}
	)
	return []mutation{
		// --- the preimage file ------------------------------------------
		{
			id: "preimages/trailing-byte", suite: "preimages",
			clause: "preimage.no-trailing-bytes", expect: "reject",
			note:   "the file ends after the final record; any trailing byte makes it invalid",
			rawPre: func(b []byte) []byte { return append(bytes.Clone(b), 0x00) },
		},
		{
			id: "preimages/truncated-record", suite: "preimages",
			clause: "preimage.record-self-delimiting", expect: "reject",
			note:   "the last record's final slot key is cut short",
			rawPre: func(b []byte) []byte { return bytes.Clone(b)[:len(b)-1] },
		},
		{
			id: "preimages/slot-count-overclaim", suite: "preimages",
			clause: "preimage.record-self-delimiting", expect: "reject",
			note: "the first record claims more slots than the file can hold",
			rawPre: func(b []byte) []byte {
				out := bytes.Clone(b)
				binary.BigEndian.PutUint32(out[common.AddressLength:], 0xffff)
				return out
			},
		},
		{
			id: "preimages/raw-address-order", suite: "preimages",
			clause: "preimage.hashed-key-order", expect: "reject",
			note: "records sorted by raw address rather than by keccak256(address)",
			rawPre: func(b []byte) []byte {
				recs, err := decodePreimages(b)
				if err != nil {
					panic(err)
				}
				slices.SortStableFunc(recs, func(x, y record) int { return bytes.Compare(x.addr[:], y.addr[:]) })
				return encodeRecordsVerbatim(recs, nil)
			},
		},
		{
			id: "preimages/raw-slot-order", suite: "preimages",
			clause: "preimage.hashed-key-order", expect: "reject",
			note: "slot keys sorted by slot number rather than by keccak256(slotKey)",
			rawPre: func(b []byte) []byte {
				recs, err := decodePreimages(b)
				if err != nil {
					panic(err)
				}
				return encodeRecordsVerbatim(recs, func(slots []common.Hash) []common.Hash {
					out := slices.Clone(slots)
					slices.SortStableFunc(out, func(x, y common.Hash) int { return bytes.Compare(x[:], y[:]) })
					return out
				})
			},
		},
		{
			id: "preimages/duplicate-address", suite: "preimages",
			clause: "preimage.address-appears-once", expect: "reject",
			note: "one account carries two records",
			rawPre: func(b []byte) []byte {
				recs, err := decodePreimages(b)
				if err != nil {
					panic(err)
				}
				return encodeRecordsVerbatim(append(recs, recs[0]), nil)
			},
		},
		{
			id: "preimages/duplicate-slot", suite: "preimages",
			clause: "preimage.no-duplicate-slots", expect: "reject",
			note: "a storage-holding account names one slot twice",
			rawPre: func(b []byte) []byte {
				recs, err := decodePreimages(b)
				if err != nil {
					panic(err)
				}
				i := findRecord(recs, storageSpread)
				recs[i].slots = append(recs[i].slots, recs[i].slots[0])
				return encodeRecordsVerbatim(recs, nil)
			},
		},
		{
			id: "preimages/missing-account", suite: "preimages",
			clause: "converter.preimage-set-matches-leaves", expect: "reject",
			note:      "an account the snapshot carries has no preimage record",
			preimages: func(recs []record) []record { return deleteRecord(recs, eoaBalance) },
		},
		{
			id: "preimages/surplus-account", suite: "preimages",
			clause: "converter.preimage-set-matches-leaves", expect: "reject",
			note: "a preimage record names an account the state does not hold",
			preimages: func(recs []record) []record {
				return append(recs, record{addr: common.HexToAddress("0x00000000000000000000000000000000deadbeef")})
			},
		},
		{
			id: "preimages/missing-header-slot", suite: "preimages",
			clause: "converter.preimage-set-matches-leaves", expect: "reject",
			note: "a header-range slot the state holds is absent from the file",
			preimages: func(recs []record) []record {
				i := findRecord(recs, storageSpread)
				recs[i].slots = slices.DeleteFunc(recs[i].slots, func(h common.Hash) bool {
					return h == common.BigToHash(big.NewInt(63))
				})
				return recs
			},
		},
		{
			id: "preimages/surplus-header-slot", suite: "preimages",
			clause: "converter.preimage-set-matches-leaves", expect: "reject",
			note: "the file names a header-range slot the state does not hold",
			preimages: func(recs []record) []record {
				i := findRecord(recs, storageSpread)
				recs[i].slots = append(recs[i].slots, common.BigToHash(big.NewInt(7)))
				return recs
			},
		},
		{
			id: "preimages/missing-overflow-slot", suite: "preimages",
			clause: "converter.preimage-set-matches-leaves", expect: "reject",
			note: "an overflow slot the state holds is absent, so its leaf can never be keyed",
			preimages: func(recs []record) []record {
				i := findRecord(recs, storageSpread)
				recs[i].slots = slices.DeleteFunc(recs[i].slots, func(h common.Hash) bool {
					return h == common.BigToHash(big.NewInt(256))
				})
				return recs
			},
		},
		{
			id: "preimages/empty-file", suite: "preimages",
			clause: "converter.preimage-set-matches-leaves", expect: "reject",
			note:   "no preimages at all against a populated snapshot",
			rawPre: func([]byte) []byte { return nil },
		},

		// --- the snapshot: header and encoding ---------------------------
		{
			id: "snapshot/wrong-claimed-root", suite: "snapshot",
			clause: "snapshot.root-recomputed", expect: "reject",
			note: "the header claims a root the leaves do not fold to",
			rawSnap: func(b []byte) []byte {
				out := bytes.Clone(b)
				out[31] ^= 0x01
				return out
			},
		},
		{
			id: "snapshot/leaf-count-high", suite: "snapshot",
			clause: "snapshot.leaf-count-recomputed", expect: "reject",
			note:    "the header claims one leaf more than the file carries",
			rawSnap: func(b []byte) []byte { return withCount(b, uint64(len(valid.leaves))+1) },
		},
		{
			id: "snapshot/leaf-count-low", suite: "snapshot",
			clause: "snapshot.leaf-count-recomputed", expect: "reject",
			note:    "the header claims one leaf fewer than the file carries",
			rawSnap: func(b []byte) []byte { return withCount(b, uint64(len(valid.leaves))-1) },
		},
		{
			id: "snapshot/truncated-stream", suite: "snapshot",
			clause: "snapshot.record-self-delimiting", expect: "reject",
			note:    "the final record is cut short",
			rawSnap: func(b []byte) []byte { return bytes.Clone(b)[:len(b)-1] },
		},
		{
			id: "snapshot/trailing-garbage", suite: "snapshot",
			clause: "snapshot.leaf-count-recomputed", expect: "reject",
			note:    "bytes follow the last record the header accounts for",
			rawSnap: func(b []byte) []byte { return append(bytes.Clone(b), 0xde, 0xad) },
		},
		{
			id: "snapshot/records-out-of-order", suite: "snapshot",
			clause: "snapshot.ascending-key-order", expect: "reject",
			note: "two adjacent records are swapped, so the stream is not ascending",
			snapshot: func(leaves []leaf) []leaf {
				leaves[0], leaves[1] = leaves[1], leaves[0]
				return leaves
			},
			keepRoot: true,
		},
		{
			id: "snapshot/duplicate-key", suite: "snapshot",
			clause: "snapshot.ascending-key-order", expect: "reject",
			note: "one key appears twice, which ascending order forbids",
			snapshot: func(leaves []leaf) []leaf {
				return slices.Insert(leaves, 1, leaves[0])
			},
			keepRoot: true,
		},
		{
			id: "snapshot/reserved-zone", suite: "snapshot",
			clause: "snapshot.zone-byte", expect: "reject",
			note: "a key sits in the reserved 0x02-0xFE zone range",
			snapshot: func(leaves []leaf) []leaf {
				leaves[0].key = bytes.Clone(leaves[0].key)
				leaves[0].key[0] = 0x02
				return leaves
			},
			keepRoot: true,
		},
		{
			id: "snapshot/wrong-key-length", suite: "snapshot",
			clause: "snapshot.zone-fixes-key-length", expect: "reject",
			note: "an account-zone key carries a storage-zone key length",
			snapshot: func(leaves []leaf) []leaf {
				i := findPrefix(leaves, []byte{bintrie.AccountZone})
				leaves[i].key = append(bytes.Clone(leaves[i].key), make([]byte, bintrie.StorageKeyLength-bintrie.AccountKeyLength)...)
				return leaves
			},
			keepRoot: true,
		},
		{
			id: "snapshot/zero-value-present", suite: "snapshot",
			clause: "snapshot.no-zero-values", expect: "reject",
			note: "a leaf holds 32 zero bytes, which EIP-8297 requires to be absent",
			snapshot: func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{key: bintrie.HeaderKey(eoaBalance, 3), value: zeroValue})
			},
			keepRoot: true,
		},
		{
			id: "snapshot/non-canonical-value", suite: "snapshot",
			clause: "snapshot.canonical-integer-value", expect: "reject",
			note:    "a value is encoded with a leading zero byte",
			rawSnap: func(b []byte) []byte { return padFirstValue(b) },
		},

		// --- the snapshot: what the leaves say about the state -----------
		{
			id: "snapshot/flipped-value-root-kept", suite: "snapshot",
			clause: "verification.internal-consistency", expect: "reject",
			note: "a leaf value is altered and the claimed root left alone, so the rebuild disagrees",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				leaves[i].value[31] ^= 0x01
				return leaves
			},
			keepRoot: true,
		},
		{
			id: "snapshot/flipped-value-root-recomputed", suite: "snapshot",
			clause: "verification.consensus-anchoring", expect: "reject",
			note: "a balance is altered and the root recomputed, so only the MPT re-hash catches it",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				leaves[i].value[31] ^= 0x01
				return leaves
			},
		},
		{
			id: "snapshot/nonzero-version", suite: "snapshot",
			clause: "embedding.version-zero", expect: "reject",
			note: "a basic-data leaf carries a non-zero version byte",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				leaves[i].value[0] = 1
				return leaves
			},
		},
		{
			id: "snapshot/basic-data-reserved-garbage", suite: "snapshot",
			clause: "embedding.basic-data-layout", expect: "reject",
			note: "the three reserved bytes of a basic-data leaf are not zero",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				leaves[i].value[1], leaves[i].value[2], leaves[i].value[3] = 0xff, 0xff, 0xff
				return leaves
			},
		},
		{
			id: "snapshot/wrong-code-size", suite: "snapshot",
			clause: "verification.code-limb", expect: "reject",
			note: "a contract's code_size disagrees with the chunks its code hash covers",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(codeChunkExact))
				binary.BigEndian.PutUint32(leaves[i].value[4:8], 7)
				return leaves
			},
		},
		{
			id: "snapshot/missing-code-chunk", suite: "snapshot",
			clause: "verification.code-limb", expect: "reject",
			note: "one code leaf is dropped, so the reassembled code does not hash to its code hash",
			snapshot: func(leaves []leaf) []leaf {
				return slices.Delete(leaves, findPrefix(leaves, []byte{bintrie.CodeZone}), findPrefix(leaves, []byte{bintrie.CodeZone})+1)
			},
		},
		{
			id: "snapshot/flipped-pushdata-byte", suite: "snapshot",
			clause: "verification.code-limb", expect: "reject",
			note: "a code chunk's leading-PUSHDATA count is wrong while its code bytes are intact",
			snapshot: func(leaves []leaf) []leaf {
				i := findPrefix(leaves, []byte{bintrie.CodeZone})
				leaves[i].value[0] ^= 0x01
				return leaves
			},
		},
		{
			id: "snapshot/orphan-code-leaves", suite: "snapshot",
			clause: "verification.code-limb", expect: "reject",
			note: "code leaves under a code hash no account holds",
			snapshot: func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{
					key:   bintrie.CodeChunkKey(crypto.Keccak256Hash([]byte("orphan")), 0),
					value: oneValue,
				})
			},
		},
		{
			id: "snapshot/delegation-and-code-hash", suite: "snapshot",
			clause: "embedding.one-of-codehash-or-delegation", expect: "reject",
			note: "a delegated account also carries a code-hash leaf",
			snapshot: func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{key: bintrie.CodeHashKey(delegatedA), value: emptyCodeHash()})
			},
		},
		{
			id: "snapshot/delegation-wrong-code-size", suite: "snapshot",
			clause: "embedding.delegation-code-size-23", expect: "reject",
			note: "a delegated account's code_size is not 23",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(delegatedA))
				binary.BigEndian.PutUint32(leaves[i].value[4:8], 24)
				return leaves
			},
		},
		{
			id: "snapshot/delegation-padding-garbage", suite: "snapshot",
			clause: "embedding.delegation-leaf-layout", expect: "reject",
			note: "the nine bytes after a delegation designator are not zero",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.DelegationKey(delegatedA))
				leaves[i].value[31] = 0xff
				return leaves
			},
		},
		{
			id: "snapshot/header-slot-in-storage-zone", suite: "snapshot",
			clause: "embedding.header-holds-slots-0-63", expect: "reject",
			note: "a slot below 64 is keyed in the storage zone instead of the header stem",
			snapshot: func(leaves []leaf) []leaf {
				// The storage-zone key for slot 0 is the one for slot 64 with
				// its sub-index zeroed: both sit in tree_index 0, and the
				// embedding sends only slot 64 there.
				overflow := bytes.Clone(leaves[findKey(leaves, bintrie.StorageSlotKey(storageSpread, common.BigToHash(big.NewInt(64)).Bytes()))].key)
				overflow[len(overflow)-1] = 0

				i := findKey(leaves, bintrie.HeaderKey(storageSpread, bintrie.HeaderStorageOffset))
				moved := leaf{key: overflow, value: leaves[i].value}
				return insertSorted(slices.Delete(leaves, i, i+1), moved)
			},
		},
		{
			id: "snapshot/anchored-elsewhere", suite: "snapshot",
			clause: "verification.consensus-anchoring", expect: "reject",
			note: "an internally consistent snapshot of a different state, which only the anchor check refuses",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.BasicDataKey(eoaBalance))
				return slices.Delete(leaves, i, i+1)
			},
		},

		// --- clauses the EIP leaves open --------------------------------
		{
			id: "snapshot/empty", suite: "snapshot",
			clause: "unspecified.empty-snapshot", expect: "unspecified",
			note:    "leafCount 0: the reference converter refuses it, the EIP does not say",
			rawSnap: func(b []byte) []byte { return withCount(bytes.Clone(b)[:snapshotHeaderSize], 0) },
		},
		{
			id: "snapshot/reserved-header-subindex", suite: "snapshot",
			clause: "unspecified.header-subindex-gap", expect: "unspecified",
			note: "a leaf at header sub-index 3, in the gap between delegation and storage",
			snapshot: func(leaves []leaf) []leaf {
				return insertSorted(leaves, leaf{key: bintrie.HeaderKey(eoaBalance, 3), value: oneValue})
			},
		},
		{
			id: "snapshot/codeless-account-without-code-hash", suite: "snapshot",
			clause: "unspecified.codehash-leaf-required", expect: "unspecified",
			note: "an account with no code drops its code-hash leaf, which a verifier may infer",
			snapshot: func(leaves []leaf) []leaf {
				i := findKey(leaves, bintrie.CodeHashKey(eoaBalance))
				return slices.Delete(leaves, i, i+1)
			},
		},
		{
			id: "preimages/storage-less-record-absent", suite: "preimages",
			clause: "unspecified.slotcount-zero-record", expect: "unspecified",
			note:      "an account with no storage is left out of the file entirely",
			preimages: func(recs []record) []record { return deleteRecord(recs, precompile) },
		},
	}
}

// encodeRecordsVerbatim writes the records in the order given, without the
// sort the valid encoder applies, so a case can put them out of order. An
// optional hook reorders each record's slots the same way.
func encodeRecordsVerbatim(recs []record, slotOrder func([]common.Hash) []common.Hash) []byte {
	var buf bytes.Buffer
	for _, r := range recs {
		slots := r.slots
		if slotOrder != nil {
			slots = slotOrder(slots)
		}
		buf.Write(r.addr[:])
		buf.Write(binary.BigEndian.AppendUint32(nil, uint32(len(slots))))
		for _, s := range slots {
			buf.Write(s[:])
		}
	}
	return buf.Bytes()
}

func deleteRecord(recs []record, addr common.Address) []record {
	return slices.DeleteFunc(recs, func(r record) bool { return r.addr == addr })
}

// withCount rewrites the header's leaf count.
func withCount(b []byte, count uint64) []byte {
	out := bytes.Clone(b)
	binary.BigEndian.PutUint64(out[32:snapshotHeaderSize], count)
	return out
}

// padFirstValue re-encodes the first record with a leading zero byte in its
// value, an encoding the writers can never emit.
func padFirstValue(b []byte) []byte {
	root, leaves, err := decodeSnapshot(b)
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	buf.Write(root[:])
	buf.Write(binary.BigEndian.AppendUint64(nil, uint64(len(leaves))))
	for i, l := range leaves {
		if i == 0 {
			padded := append([]byte{0x00}, common.TrimLeftZeroes(l.value[:])...)
			buf.Write(mustRLP(l.key, padded))
			continue
		}
		buf.Write(encodeLeaf(l))
	}
	return buf.Bytes()
}

func emptyCodeHash() [32]byte {
	return [32]byte(crypto.Keccak256(nil))
}
