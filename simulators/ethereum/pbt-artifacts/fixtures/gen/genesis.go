package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// The anchor state. Every account here exists to make one EIP-8297 embedding
// rule observable in the artifacts, so that a client consuming them has to
// have implemented that rule rather than the common case only. Addresses are
// spelled out so the same fixture can be read against a hex dump.
var (
	eoaBalance     = common.HexToAddress("0x0000000000000000000000000000000000000101")
	eoaNonce       = common.HexToAddress("0x0000000000000000000000000000000000000102")
	eoaMaxima      = common.HexToAddress("0x0000000000000000000000000000000000000103")
	precompile     = common.HexToAddress("0x0000000000000000000000000000000000000004")
	codeOneByte    = common.HexToAddress("0x0000000000000000000000000000000000000201")
	codeChunkExact = common.HexToAddress("0x0000000000000000000000000000000000000202")
	codeChunkPlus  = common.HexToAddress("0x0000000000000000000000000000000000000203")
	codeStraddle   = common.HexToAddress("0x0000000000000000000000000000000000000204")
	codeZeroChunk  = common.HexToAddress("0x0000000000000000000000000000000000000205")
	codeAllZero    = common.HexToAddress("0x0000000000000000000000000000000000000206")
	codeTwoGroups  = common.HexToAddress("0x0000000000000000000000000000000000000207")
	codeFakeDelega = common.HexToAddress("0x0000000000000000000000000000000000000209")
	sharedA        = common.HexToAddress("0x0000000000000000000000000000000000000301")
	sharedB        = common.HexToAddress("0x0000000000000000000000000000000000000302")
	delegatedA     = common.HexToAddress("0x0000000000000000000000000000000000000401")
	delegatedB     = common.HexToAddress("0x0000000000000000000000000000000000000402")
	delegatedC     = common.HexToAddress("0x0000000000000000000000000000000000000403")
	delegateTarget = common.HexToAddress("0x00000000000000000000000000000000000004ff")
	storageSpread  = common.HexToAddress("0x0000000000000000000000000000000000000501")
	storageValues  = common.HexToAddress("0x0000000000000000000000000000000000000502")
	storageOnEOA   = common.HexToAddress("0x0000000000000000000000000000000000000503")
)

// chunk is 31 bytes of code, the payload of one PBT code leaf.
const chunk = 31

// maxUint128 is the largest balance the basic-data leaf's 16-byte field can
// hold, so it is the largest one a conversion can represent at all.
var maxUint128 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

// fill returns n bytes of JUMPDEST, which chunk to non-zero values and carry
// no PUSHDATA, so every chunk of such code is present in the tree.
func fill(n int) []byte {
	code := make([]byte, n)
	for i := range code {
		code[i] = 0x5b
	}
	return code
}

// straddlingPush returns code whose PUSH32 operand crosses a chunk boundary,
// so the second chunk must record a non-zero leading-PUSHDATA count.
func straddlingPush() []byte {
	code := fill(2 * chunk)
	// PUSH32 lands two bytes before the boundary: 29 of its 32 operand bytes
	// fall in the next chunk, which must say so in its first byte.
	code[chunk-2] = 0x7f
	for i := chunk - 1; i < chunk+30 && i < len(code); i++ {
		code[i] = 0xaa
	}
	return code
}

// zeroChunkCode returns code whose second chunk is 31 zero bytes with no
// PUSHDATA running into it, so EIP-8297 requires that leaf to be absent while
// the chunks either side of it are present.
func zeroChunkCode() []byte {
	code := make([]byte, 3*chunk)
	copy(code, fill(chunk))
	copy(code[2*chunk:], fill(chunk))
	return code
}

// twoGroupCode returns code longer than one code group (256 chunks), so its
// leaves span two CODE_ZONE stems.
func twoGroupCode() []byte { return fill(257*chunk + 1) }

// delegation returns the EIP-7702 indicator for target.
func delegation(target common.Address) []byte {
	return append([]byte{0xef, 0x01, 0x00}, target.Bytes()...)
}

// slot is a 32-byte big-endian storage slot number.
func slot(n *big.Int) common.Hash { return common.BigToHash(n) }

func pow2(n uint) *big.Int { return new(big.Int).Lsh(big.NewInt(1), n) }

// edgeCaseAlloc is the anchor state: one entry per embedding rule.
func edgeCaseAlloc() types.GenesisAlloc {
	shared := fill(3 * chunk)
	maxSlot := new(big.Int).Sub(pow2(256), big.NewInt(1))

	alloc := types.GenesisAlloc{
		// Accounts with no code: the MPT commits a code hash for them too, so
		// each must carry a code-hash leaf holding the hash of empty code.
		eoaBalance: {Balance: big.NewInt(1_000_000)},
		eoaNonce:   {Balance: big.NewInt(0), Nonce: 7},
		eoaMaxima:  {Balance: maxUint128, Nonce: ^uint64(0)},
		precompile: {Balance: big.NewInt(1)},

		// Code-size boundaries around the 31-byte chunk.
		codeOneByte:    {Balance: big.NewInt(1), Code: fill(1)},
		codeChunkExact: {Balance: big.NewInt(1), Code: fill(chunk)},
		codeChunkPlus:  {Balance: big.NewInt(1), Code: fill(chunk + 1)},
		codeStraddle:   {Balance: big.NewInt(1), Code: straddlingPush()},

		// Zero chunks: absent when they carry no PUSHDATA, and an account
		// whose code is entirely zeros has no code leaves at all while still
		// holding a non-zero code size and a code hash.
		codeZeroChunk: {Balance: big.NewInt(1), Code: zeroChunkCode()},
		codeAllZero:   {Balance: big.NewInt(1), Code: make([]byte, 2*chunk)},

		// Code spanning two code groups, so the chunk index crosses its
		// sub-index byte and a second CODE_ZONE stem is needed. The EIP-170
		// maximum size is deliberately not here: it adds 793 code leaves to
		// every fixture copy and exercises nothing this does not.
		codeTwoGroups: {Balance: big.NewInt(1), Code: twoGroupCode()},

		// Code that begins with the delegation marker but is not an
		// indicator: it is ordinary code, and must be chunked as such.
		codeFakeDelega: {Balance: big.NewInt(1), Code: append(delegation(delegateTarget), fill(1)...)},

		// Identical bytecode under two accounts: the code leaves are
		// content-addressed, so they must appear exactly once.
		sharedA: {Balance: big.NewInt(1), Code: shared},
		sharedB: {Balance: big.NewInt(2), Code: shared},

		// Delegations: two to the same target, one to another, and one that
		// also holds storage, a nonce and a balance.
		delegatedA: {Balance: big.NewInt(1), Code: delegation(delegateTarget)},
		delegatedB: {Balance: big.NewInt(1), Code: delegation(delegateTarget)},
		delegatedC: {
			Balance: big.NewInt(3), Nonce: 4, Code: delegation(sharedA),
			Storage: map[common.Hash]common.Hash{
				slot(big.NewInt(1)): common.BigToHash(big.NewInt(0x11)),
			},
		},
		delegateTarget: {Balance: big.NewInt(1), Code: fill(chunk)},

		// Storage either side of the header boundary, at the sub-index
		// boundary, and in several storage groups.
		storageSpread: {
			Balance: big.NewInt(1),
			Code:    fill(chunk),
			Storage: map[common.Hash]common.Hash{
				slot(big.NewInt(0)):   common.BigToHash(big.NewInt(0xa0)),
				slot(big.NewInt(63)):  common.BigToHash(big.NewInt(0xa1)),
				slot(big.NewInt(64)):  common.BigToHash(big.NewInt(0xa2)),
				slot(big.NewInt(255)): common.BigToHash(big.NewInt(0xa3)),
				slot(big.NewInt(256)): common.BigToHash(big.NewInt(0xa4)),
				slot(big.NewInt(511)): common.BigToHash(big.NewInt(0xa5)),
				slot(big.NewInt(512)): common.BigToHash(big.NewInt(0xa6)),
				slot(maxSlot):         common.BigToHash(big.NewInt(0xa7)),
			},
		},

		// Storage values: the smallest, the largest, and one with leading
		// zero bytes, which the snapshot encodes as a canonical integer.
		storageValues: {
			Balance: big.NewInt(1),
			Code:    fill(chunk),
			Storage: map[common.Hash]common.Hash{
				slot(big.NewInt(1)):  common.BigToHash(big.NewInt(1)),
				slot(big.NewInt(2)):  common.BigToHash(new(big.Int).Sub(pow2(256), big.NewInt(1))),
				slot(big.NewInt(3)):  common.BigToHash(pow2(8)),
				slot(big.NewInt(4)):  common.BigToHash(pow2(248)),
				slot(big.NewInt(65)): common.BigToHash(big.NewInt(0xff)),
			},
		},

		// An account with storage but no code, which the embedding treats no
		// differently from a contract's storage.
		storageOnEOA: {
			Balance: big.NewInt(1),
			Storage: map[common.Hash]common.Hash{
				slot(big.NewInt(0)):  common.BigToHash(big.NewInt(1)),
				slot(big.NewInt(64)): common.BigToHash(big.NewInt(2)),
			},
		},
	}
	return alloc
}

// derivePreimages lays out the EIP-8347 preimage file from the allocation
// alone: fixed-width records ordered by keccak256(address), each account's
// slot keys at their full 32 bytes ordered by keccak256(slotKey), nothing
// between records and nothing after the last one.
//
// Deriving it rather than taking a producer's word for it is what keeps the
// canonical bytes independent of every client being measured against them.
func derivePreimages(alloc types.GenesisAlloc) []byte {
	byHash := func(a, b []byte) int { return bytes.Compare(crypto.Keccak256(a), crypto.Keccak256(b)) }

	addrs := make([]common.Address, 0, len(alloc))
	for addr := range alloc {
		addrs = append(addrs, addr)
	}
	slices.SortFunc(addrs, func(a, b common.Address) int { return byHash(a[:], b[:]) })

	var buf bytes.Buffer
	for _, addr := range addrs {
		slots := make([]common.Hash, 0, len(alloc[addr].Storage))
		for slot := range alloc[addr].Storage {
			slots = append(slots, slot)
		}
		slices.SortFunc(slots, func(a, b common.Hash) int { return byHash(a[:], b[:]) })

		buf.Write(addr[:])
		buf.Write(binary.BigEndian.AppendUint32(nil, uint32(len(slots))))
		for _, slot := range slots {
			buf.Write(slot[:])
		}
	}
	return buf.Bytes()
}

// genesisJSON renders the allocation as the genesis file every client is
// initialised from. The fork schedule is the one hive's own chains use, so
// each client's existing mapper accepts it unchanged.
func genesisJSON(alloc types.GenesisAlloc) ([]byte, error) {
	type genesis struct {
		Config     map[string]any     `json:"config"`
		Nonce      string             `json:"nonce"`
		Timestamp  string             `json:"timestamp"`
		ExtraData  string             `json:"extraData"`
		GasLimit   string             `json:"gasLimit"`
		Difficulty string             `json:"difficulty"`
		MixHash    string             `json:"mixHash"`
		Coinbase   string             `json:"coinbase"`
		Alloc      types.GenesisAlloc `json:"alloc"`
	}
	g := genesis{
		Config: map[string]any{
			"chainId":                 7347,
			"homesteadBlock":          0,
			"eip150Block":             0,
			"eip155Block":             0,
			"eip158Block":             0,
			"byzantiumBlock":          0,
			"constantinopleBlock":     0,
			"petersburgBlock":         0,
			"istanbulBlock":           0,
			"muirGlacierBlock":        0,
			"berlinBlock":             0,
			"londonBlock":             0,
			"arrowGlacierBlock":       0,
			"grayGlacierBlock":        0,
			"mergeNetsplitBlock":      0,
			"shanghaiTime":            0,
			"cancunTime":              0,
			"terminalTotalDifficulty": 0,
			"ethash":                  map[string]any{},
			"blobSchedule": map[string]any{
				"cancun": map[string]any{"target": 3, "max": 6, "baseFeeUpdateFraction": 3338477},
			},
		},
		Nonce:      "0x0",
		Timestamp:  "0x0",
		ExtraData:  "0x7062742d617274696661637473",
		GasLimit:   "0x23f3e20",
		Difficulty: "0x1",
		MixHash:    "0x0000000000000000000000000000000000000000000000000000000000000000",
		Coinbase:   "0x0000000000000000000000000000000000000000",
		Alloc:      alloc,
	}
	return json.MarshalIndent(g, "", "  ")
}

func writeGenesis(path string, alloc types.GenesisAlloc) error {
	blob, err := genesisJSON(alloc)
	if err != nil {
		return fmt.Errorf("rendering genesis: %w", err)
	}
	return os.WriteFile(path, append(blob, '\n'), 0644)
}
