package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	snapshotHeaderSize       = 40
	preimageRecordHeaderSize = common.AddressLength + 4
)

// encodeLeaf is one snapshot record: RLP of the full key and the value as a
// canonical integer, which is the 32-byte value with its leading zero bytes
// removed.
func encodeLeaf(l leaf) []byte {
	blob, err := rlp.EncodeToBytes([]any{l.key, common.TrimLeftZeroes(l.value[:])})
	if err != nil {
		panic(err)
	}
	return blob
}

// mustRLP encodes one record verbatim, so a case can hand the encoder bytes
// the writers would never produce.
func mustRLP(key, value []byte) []byte {
	blob, err := rlp.EncodeToBytes([]any{key, value})
	if err != nil {
		panic(err)
	}
	return blob
}

// decodeSnapshot reads the artifact back into records, enforcing only what it
// needs to hand mutations a faithful copy; the rules themselves are what the
// clients under test are being measured against.
func decodeSnapshot(blob []byte) (common.Hash, []leaf, error) {
	if len(blob) < snapshotHeaderSize {
		return common.Hash{}, nil, fmt.Errorf("snapshot is %d bytes, shorter than its header", len(blob))
	}
	root := common.BytesToHash(blob[:32])
	count := binary.BigEndian.Uint64(blob[32:snapshotHeaderSize])

	stream := rlp.NewStream(bytes.NewReader(blob[snapshotHeaderSize:]), uint64(len(blob)))
	var leaves []leaf
	for {
		var rec struct {
			Key   []byte
			Value []byte
		}
		if err := stream.Decode(&rec); err == io.EOF {
			break
		} else if err != nil {
			return common.Hash{}, nil, fmt.Errorf("record %d: %w", len(leaves), err)
		}
		var value [32]byte
		copy(value[32-len(rec.Value):], rec.Value)
		leaves = append(leaves, leaf{key: rec.Key, value: value})
	}
	if uint64(len(leaves)) != count {
		return common.Hash{}, nil, fmt.Errorf("header claims %d leaves, the file holds %d", count, len(leaves))
	}
	return root, leaves, nil
}

// decodePreimages reads the preimage file back into records.
func decodePreimages(blob []byte) ([]record, error) {
	var recs []record
	for len(blob) > 0 {
		if len(blob) < preimageRecordHeaderSize {
			return nil, fmt.Errorf("record %d is truncated", len(recs))
		}
		addr := common.BytesToAddress(blob[:common.AddressLength])
		count := int(binary.BigEndian.Uint32(blob[common.AddressLength:preimageRecordHeaderSize]))
		blob = blob[preimageRecordHeaderSize:]
		if len(blob) < count*common.HashLength {
			return nil, fmt.Errorf("record %x claims %d slots, %d bytes remain", addr, count, len(blob))
		}
		slots := make([]common.Hash, count)
		for i := range slots {
			slots[i] = common.BytesToHash(blob[:common.HashLength])
			blob = blob[common.HashLength:]
		}
		recs = append(recs, record{addr: addr, slots: slots})
	}
	return recs, nil
}

// findKey returns the index of the leaf with the given key.
func findKey(leaves []leaf, key []byte) int {
	for i, l := range leaves {
		if bytes.Equal(l.key, key) {
			return i
		}
	}
	panic(fmt.Sprintf("no leaf at key %x", key))
}

// findPrefix returns the index of the first leaf whose key starts with the
// given prefix, so a case can name "some code leaf" without knowing which.
func findPrefix(leaves []leaf, prefix []byte) int {
	for i, l := range leaves {
		if bytes.HasPrefix(l.key, prefix) {
			return i
		}
	}
	panic(fmt.Sprintf("no leaf under prefix %x", prefix))
}

// insertSorted puts a leaf in PBT-key order, which is where a well-formed
// artifact would carry it.
func insertSorted(leaves []leaf, l leaf) []leaf {
	i := 0
	for i < len(leaves) && bytes.Compare(leaves[i].key, l.key) < 0 {
		i++
	}
	return append(leaves[:i:i], append([]leaf{l}, leaves[i:]...)...)
}

// findRecord returns the index of the preimage record for addr.
func findRecord(recs []record, addr common.Address) int {
	for i, r := range recs {
		if r.addr == addr {
			return i
		}
	}
	panic(fmt.Sprintf("no preimage record for %x", addr))
}
