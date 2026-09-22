// Command gen regenerates the checked-in EIP-8347 artifact fixtures.
//
// It writes the anchor genesis, produces the valid artifacts by running the
// reference converter over it, then derives one file per way an artifact can
// lie. The result is a manifest naming every case and its expected outcome,
// which the simulator reads; the simulator itself never runs this code.
//
// Usage:
//
//	go run . -geth /path/to/geth -out ../
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie/bintrie"
)

func main() {
	var (
		gethBin = flag.String("geth", "geth", "path to a geth binary built from the PBT fork")
		outDir  = flag.String("out", "..", "fixtures directory to write")
	)
	flag.Parse()

	if err := run(*gethBin, *outDir); err != nil {
		log.Fatal(err)
	}
}

func run(gethBin, outDir string) error {
	alloc := edgeCaseAlloc()
	genesisPath := filepath.Join(outDir, "genesis.json")
	if err := writeGenesis(genesisPath, alloc); err != nil {
		return err
	}
	fmt.Printf("genesis.json: %d accounts\n", len(alloc))

	valid, err := convert(gethBin, genesisPath, outDir, alloc)
	if err != nil {
		return err
	}
	fmt.Printf("valid artifacts: pbtRoot %x, %d leaves, %d preimage records\n",
		valid.root, len(valid.leaves), len(valid.records))

	cases, err := writeCases(outDir, valid)
	if err != nil {
		return err
	}
	return writeManifest(outDir, genesisPath, valid, cases)
}

// artifacts is the valid pair, decoded, so mutations work on records rather
// than on bytes.
type artifacts struct {
	root       common.Hash
	stateRoot  common.Hash
	leaves     []leaf
	records    []record
	snapshotFD string
	preimageFD string
}

type leaf struct {
	key   []byte
	value [32]byte
}

type record struct {
	addr  common.Address
	slots []common.Hash
}

// convert initialises a throwaway datadir from the genesis and runs the
// reference converter over it, then decodes what it produced and holds its
// preimage file to the one the allocation implies.
func convert(gethBin, genesisPath, outDir string, alloc types.GenesisAlloc) (*artifacts, error) {
	datadir, err := os.MkdirTemp("", "pbt-fixtures-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(datadir)

	// The converter reads the plain keys behind the trie paths, so the state
	// has to be written with the preimage store on.
	if out, err := exec.Command(gethBin, "--datadir", datadir, "--cache.preimages", "init", genesisPath).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("geth init: %w\n%s", err, out)
	}
	var (
		validDir = filepath.Join(outDir, "valid")
		snapPath = filepath.Join(validDir, "snapshot.bin")
		prePath  = filepath.Join(validDir, "preimages.bin")
	)
	if err := os.MkdirAll(validDir, 0755); err != nil {
		return nil, err
	}
	cmd := exec.Command(gethBin, "--datadir", datadir, "bintrie", "convert",
		"--snapshot-out", snapPath, "--preimages-out", prePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("geth bintrie convert: %w\n%s", err, out)
	}
	stateRoot, err := genesisStateRoot(genesisPath)
	if err != nil {
		return nil, err
	}

	snapBlob, err := os.ReadFile(snapPath)
	if err != nil {
		return nil, err
	}
	preBlob, err := os.ReadFile(prePath)
	if err != nil {
		return nil, err
	}
	// The preimage file is derivable from the allocation alone: the EIP fixes
	// the framing and the order, and the allocation names every address and
	// slot key. So it is derived here rather than taken from the converter,
	// and the converter is held to it. That keeps the canonical bytes
	// independent of any client, which matters because these bytes are what
	// every client is then measured against.
	want := derivePreimages(alloc)
	if !bytes.Equal(preBlob, want) {
		return nil, fmt.Errorf("the converter's preimage file disagrees with the layout the state implies:\nconverter %x\nderived   %x", preBlob, want)
	}
	root, leaves, err := decodeSnapshot(snapBlob)
	if err != nil {
		return nil, fmt.Errorf("the converter's own snapshot does not decode: %w", err)
	}
	records, err := decodePreimages(preBlob)
	if err != nil {
		return nil, fmt.Errorf("the derived preimage file does not decode: %w", err)
	}
	// The root the converter claims must be the root its leaves fold to: the
	// fixtures are worthless if the pair disagrees before a mutation.
	if got := foldRoot(leaves); got != root {
		return nil, fmt.Errorf("valid snapshot claims root %x, its leaves fold to %x", root, got)
	}
	return &artifacts{
		root: root, stateRoot: stateRoot, leaves: leaves, records: records,
		snapshotFD: snapPath, preimageFD: prePath,
	}, nil
}

// genesisStateRoot derives the anchor header's state root from the genesis
// file itself, which is what a consumer checks the re-hashed leaves against.
func genesisStateRoot(genesisPath string) (common.Hash, error) {
	blob, err := os.ReadFile(genesisPath)
	if err != nil {
		return common.Hash{}, err
	}
	var g core.Genesis
	if err := json.Unmarshal(blob, &g); err != nil {
		return common.Hash{}, fmt.Errorf("parsing the genesis we just wrote: %w", err)
	}
	return g.ToBlock().Root(), nil
}

func foldRoot(leaves []leaf) common.Hash {
	b := bintrie.NewStackBuilder(nil)
	for _, l := range leaves {
		if err := b.Add(l.key, l.value[:]); err != nil {
			panic(fmt.Sprintf("valid leaves do not fold: %v", err))
		}
	}
	return b.Finish()
}

// mutation is one way an artifact can lie.
type mutation struct {
	id     string
	suite  string // "preimages" or "snapshot"
	clause string // the rule the file breaks
	expect string // "reject" or "unspecified"
	note   string

	// Exactly one of these shapes the case.
	snapshot  func([]leaf) []leaf     // mutate the leaf set, header recomputed unless keepRoot
	preimages func([]record) []record // mutate the preimage set
	rawSnap   func([]byte) []byte     // mutate the encoded snapshot bytes
	rawPre    func([]byte) []byte     // mutate the encoded preimage bytes
	keepRoot  bool                    // keep the valid root in the header
}

func writeCases(outDir string, valid *artifacts) ([]caseEntry, error) {
	var entries []caseEntry
	for _, m := range mutations(valid) {
		dir := filepath.Join(outDir, m.suite, filepath.Base(m.id))
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
		entry := caseEntry{
			ID: m.id, Suite: m.suite, Clause: m.clause, Expect: m.expect, Note: m.note,
			Snapshot: "valid/snapshot.bin", Preimages: "valid/preimages.bin",
		}
		switch {
		case m.snapshot != nil:
			leaves := m.snapshot(cloneLeaves(valid.leaves))
			root := valid.root
			if !m.keepRoot {
				root = foldRoot(leaves)
			}
			path := filepath.Join(dir, "snapshot.bin")
			if err := os.WriteFile(path, encodeSnapshot(root, uint64(len(leaves)), leaves), 0644); err != nil {
				return nil, err
			}
			entry.Snapshot = rel(outDir, path)
		case m.preimages != nil:
			path := filepath.Join(dir, "preimages.bin")
			if err := os.WriteFile(path, encodePreimages(m.preimages(cloneRecords(valid.records))), 0644); err != nil {
				return nil, err
			}
			entry.Preimages = rel(outDir, path)
		case m.rawSnap != nil:
			blob, err := os.ReadFile(valid.snapshotFD)
			if err != nil {
				return nil, err
			}
			path := filepath.Join(dir, "snapshot.bin")
			if err := os.WriteFile(path, m.rawSnap(blob), 0644); err != nil {
				return nil, err
			}
			entry.Snapshot = rel(outDir, path)
		case m.rawPre != nil:
			blob, err := os.ReadFile(valid.preimageFD)
			if err != nil {
				return nil, err
			}
			path := filepath.Join(dir, "preimages.bin")
			if err := os.WriteFile(path, m.rawPre(blob), 0644); err != nil {
				return nil, err
			}
			entry.Preimages = rel(outDir, path)
		default:
			return nil, fmt.Errorf("case %s shapes nothing", m.id)
		}
		// A mutation that changed nothing would be scored as a client bug.
		same, err := identical(outDir, entry, valid)
		if err != nil {
			return nil, err
		}
		if same {
			return nil, fmt.Errorf("case %s produced the valid file unchanged", m.id)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func identical(outDir string, entry caseEntry, valid *artifacts) (bool, error) {
	var mutated, original string
	if entry.Snapshot != "valid/snapshot.bin" {
		mutated, original = filepath.Join(outDir, entry.Snapshot), valid.snapshotFD
	} else {
		mutated, original = filepath.Join(outDir, entry.Preimages), valid.preimageFD
	}
	a, err := os.ReadFile(mutated)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(original)
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}

func rel(outDir, path string) string {
	r, err := filepath.Rel(outDir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

func cloneLeaves(in []leaf) []leaf {
	out := make([]leaf, len(in))
	for i, l := range in {
		out[i] = leaf{key: bytes.Clone(l.key), value: l.value}
	}
	return out
}

func cloneRecords(in []record) []record {
	out := make([]record, len(in))
	for i, r := range in {
		out[i] = record{addr: r.addr, slots: slices.Clone(r.slots)}
	}
	return out
}

type caseEntry struct {
	ID        string `json:"id"`
	Suite     string `json:"suite"`
	Snapshot  string `json:"snapshot"`
	Preimages string `json:"preimages"`
	Expect    string `json:"expect"`
	Clause    string `json:"clause"`
	Note      string `json:"note,omitempty"`
}

type manifest struct {
	Spec struct {
		EIP8347   string `json:"eip8347"`
		EIP8297   string `json:"eip8297"`
		Hasher    string `json:"hasher"`
		Generator string `json:"generator"`
	} `json:"spec"`
	Genesis struct {
		File      string `json:"file"`
		StateRoot string `json:"stateRoot"`
		PBTRoot   string `json:"pbtRoot"`
		Accounts  int    `json:"accounts"`
	} `json:"genesis"`
	Valid struct {
		Snapshot       string `json:"snapshot"`
		Preimages      string `json:"preimages"`
		SnapshotDigest string `json:"snapshotDigest"`
		PreimageDigest string `json:"preimageDigest"`
		SnapshotSHA256 string `json:"snapshotSha256"`
		PreimageSHA256 string `json:"preimageSha256"`
		LeafCount      int    `json:"leafCount"`
		Records        int    `json:"records"`
	} `json:"valid"`
	Cases []caseEntry `json:"cases"`
}

func writeManifest(outDir, genesisPath string, valid *artifacts, cases []caseEntry) error {
	snapBlob, err := os.ReadFile(valid.snapshotFD)
	if err != nil {
		return err
	}
	preBlob, err := os.ReadFile(valid.preimageFD)
	if err != nil {
		return err
	}
	var m manifest
	m.Spec.EIP8347 = "https://github.com/ethereum/EIPs/blob/master/EIPS/eip-8347.md@2026-08-25"
	m.Spec.EIP8297 = "https://github.com/ethereum/EIPs/blob/master/EIPS/eip-8297.md@2026-09-21"
	m.Spec.Hasher = "blake3"
	m.Spec.Generator = "simulators/ethereum/pbt-artifacts/fixtures/gen"
	m.Genesis.File = "genesis.json"
	m.Genesis.StateRoot = valid.stateRoot.Hex()
	m.Genesis.PBTRoot = valid.root.Hex()
	m.Genesis.Accounts = len(valid.records)
	m.Valid.Snapshot = "valid/snapshot.bin"
	m.Valid.Preimages = "valid/preimages.bin"
	m.Valid.SnapshotDigest = crypto.Keccak256Hash(snapBlob).Hex()
	m.Valid.PreimageDigest = crypto.Keccak256Hash(preBlob).Hex()
	m.Valid.SnapshotSHA256 = "0x" + hex.EncodeToString(sha256sum(snapBlob))
	m.Valid.PreimageSHA256 = "0x" + hex.EncodeToString(sha256sum(preBlob))
	m.Valid.LeafCount = len(valid.leaves)
	m.Valid.Records = len(valid.records)
	m.Cases = cases

	blob, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("manifest: %d cases\n", len(cases))
	return os.WriteFile(filepath.Join(outDir, "manifest.json"), append(blob, '\n'), 0644)
}

func sha256sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// encodeSnapshot lays out the artifact: the claimed root, the leaf count, and
// one RLP [key, value] per leaf with the value as a canonical integer.
func encodeSnapshot(root common.Hash, count uint64, leaves []leaf) []byte {
	var buf bytes.Buffer
	buf.Write(root[:])
	buf.Write(binary.BigEndian.AppendUint64(nil, count))
	for _, l := range leaves {
		buf.Write(encodeLeaf(l))
	}
	return buf.Bytes()
}

// encodePreimages lays out the preimage file: fixed-width records, in the
// order the EIP demands, which is over the hashed keys.
func encodePreimages(recs []record) []byte {
	byHash := func(a, b []byte) int { return bytes.Compare(crypto.Keccak256(a), crypto.Keccak256(b)) }
	slices.SortStableFunc(recs, func(a, b record) int { return byHash(a.addr[:], b.addr[:]) })

	var buf bytes.Buffer
	for _, r := range recs {
		slots := slices.Clone(r.slots)
		slices.SortStableFunc(slots, func(a, b common.Hash) int { return byHash(a[:], b[:]) })
		buf.Write(r.addr[:])
		buf.Write(binary.BigEndian.AppendUint32(nil, uint32(len(slots))))
		for _, s := range slots {
			buf.Write(s[:])
		}
	}
	return buf.Bytes()
}
