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

// encodeLeaf is RLP([key, value]) with the value as a canonical integer.
func encodeLeaf(l leaf) []byte {
	blob, err := rlp.EncodeToBytes([]any{l.key, common.TrimLeftZeroes(l.value[:])})
	if err != nil {
		panic(err)
	}
	return blob
}

func mustRLP(key, value []byte) []byte {
	blob, err := rlp.EncodeToBytes([]any{key, value})
	if err != nil {
		panic(err)
	}
	return blob
}

// mustRLP1 is a one-item list where a record should be a pair.
func mustRLP1(key []byte) []byte {
	blob, err := rlp.EncodeToBytes([]any{key})
	if err != nil {
		panic(err)
	}
	return blob
}

// longFormPair is a valid pair with the value's length written in the long
// form (0xb8 n) that RLP reserves for strings of 56 bytes or more.
func longFormPair(key, value []byte) []byte {
	item := append([]byte{0xb8, byte(len(value))}, value...)
	keyEnc, err := rlp.EncodeToBytes(key)
	if err != nil {
		panic(err)
	}
	body := append(keyEnc, item...)
	if len(body) < 56 {
		return append([]byte{0xc0 + byte(len(body))}, body...)
	}
	return append([]byte{0xf8, byte(len(body))}, body...)
}

// decodeSnapshotLoose parses what the bytes allow: the header when it is
// present, then leaves until they stop decoding. It returns what it read
// alongside the error that stopped it, so a file that is malformed on
// purpose still yields a diffable shape.
func decodeSnapshotLoose(blob []byte) (root common.Hash, claimed uint64, leaves []leaf, err error) {
	if len(blob) < snapshotHeaderSize {
		return root, claimed, leaves, fmt.Errorf("snapshot is %d bytes, shorter than its header", len(blob))
	}
	root = common.BytesToHash(blob[:32])
	claimed = binary.BigEndian.Uint64(blob[32:snapshotHeaderSize])

	stream := rlp.NewStream(bytes.NewReader(blob[snapshotHeaderSize:]), uint64(len(blob)))
	for {
		var rec struct {
			Key   []byte
			Value []byte
		}
		if err := stream.Decode(&rec); err == io.EOF {
			break
		} else if err != nil {
			return root, claimed, leaves, fmt.Errorf("record %d: %w", len(leaves), err)
		}
		if len(rec.Value) > 32 {
			return root, claimed, leaves, fmt.Errorf("record %d: value is %d bytes", len(leaves), len(rec.Value))
		}
		var value [32]byte
		copy(value[32-len(rec.Value):], rec.Value)
		leaves = append(leaves, leaf{key: rec.Key, value: value})
	}
	return root, claimed, leaves, nil
}

func decodeSnapshot(blob []byte) (common.Hash, []leaf, error) {
	root, claimed, leaves, err := decodeSnapshotLoose(blob)
	if err != nil {
		return common.Hash{}, nil, err
	}
	if uint64(len(leaves)) != claimed {
		return common.Hash{}, nil, fmt.Errorf("header claims %d leaves, the file holds %d", claimed, len(leaves))
	}
	return root, leaves, nil
}

// decodePreimagesLoose parses records until the bytes stop making sense,
// returning those it read alongside the error that stopped it.
func decodePreimagesLoose(blob []byte) ([]record, error) {
	var recs []record
	for len(blob) > 0 {
		if len(blob) < preimageRecordHeaderSize {
			return recs, fmt.Errorf("record %d is truncated", len(recs))
		}
		addr := common.BytesToAddress(blob[:common.AddressLength])
		count := int(binary.BigEndian.Uint32(blob[common.AddressLength:preimageRecordHeaderSize]))
		blob = blob[preimageRecordHeaderSize:]
		if len(blob) < count*common.HashLength {
			return recs, fmt.Errorf("record %x claims %d slots, %d bytes remain", addr, count, len(blob))
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

func decodePreimages(blob []byte) ([]record, error) {
	recs, err := decodePreimagesLoose(blob)
	if err != nil {
		return nil, err
	}
	return recs, nil
}

func findKey(leaves []leaf, key []byte) int {
	for i, l := range leaves {
		if bytes.Equal(l.key, key) {
			return i
		}
	}
	panic(fmt.Sprintf("no leaf at key %x", key))
}

// insertSorted keeps PBT-key order.
func insertSorted(leaves []leaf, l leaf) []leaf {
	i := 0
	for i < len(leaves) && bytes.Compare(leaves[i].key, l.key) < 0 {
		i++
	}
	return append(leaves[:i:i], append([]leaf{l}, leaves[i:]...)...)
}

func findRecord(recs []record, addr common.Address) int {
	for i, r := range recs {
		if r.addr == addr {
			return i
		}
	}
	panic(fmt.Sprintf("no preimage record for %x", addr))
}
