// Command gen regenerates the checked-in fixtures: the anchor genesis, the
// valid artifacts from the reference converter, and one file per way an
// artifact can lie. -ref names an execution-specs checkout for the root
// gate; -check re-runs the gates on what is checked in and writes nothing.
//
//	go run . -geth /path/to/geth -ref /path/to/execution-specs -out ..
//	go run . -check [-ref /path/to/execution-specs] -out ..
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
		gethBin  = flag.String("geth", "geth", "path to a geth binary built from the PBT fork")
		outDir   = flag.String("out", "..", "fixtures directory to write")
		ref      = flag.String("ref", "", "execution-specs checkout for the spec-reference root gate")
		onlyGate = flag.Bool("check", false, "re-run the admission gates on the checked-in pairs, write nothing")
	)
	flag.Parse()

	var err error
	if *onlyGate {
		err = check(*outDir, *ref)
	} else {
		err = run(*gethBin, *outDir, *ref)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func run(gethBin, outDir, ref string) error {
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
	g, err := admit(valid, genesisPath, ref)
	if err != nil {
		return err
	}

	cases, err := writeCases(outDir, valid)
	if err != nil {
		return err
	}
	produce, err := produceCases(alloc)
	if err != nil {
		return err
	}
	return writeManifest(outDir, genesisPath, valid, g, append(cases, produce...))
}

// artifacts is the valid pair, decoded.
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

// convert runs the reference converter over the genesis and holds its output
// to what the allocation implies and to the strict decoder.
func convert(gethBin, genesisPath, outDir string, alloc types.GenesisAlloc) (*artifacts, error) {
	datadir, err := os.MkdirTemp("", "pbt-fixtures-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(datadir)

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
	if err := decodeSnapshotStrict(snapBlob); err != nil {
		return nil, fmt.Errorf("the converter's snapshot breaks a serialization rule: %w", err)
	}
	preBlob, err := os.ReadFile(prePath)
	if err != nil {
		return nil, err
	}
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
	if got := foldRoot(leaves); got != root {
		return nil, fmt.Errorf("valid snapshot claims root %x, its leaves fold to %x", root, got)
	}
	if err := checkLeaves(leaves, deriveLeaves(alloc)); err != nil {
		return nil, fmt.Errorf("converter disagrees with the embedding rules: %w", err)
	}
	return &artifacts{
		root: root, stateRoot: stateRoot, leaves: leaves, records: records,
		snapshotFD: snapPath, preimageFD: prePath,
	}, nil
}

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

// mutation is one way an artifact can lie. One hook shapes the case; a leaf
// hook may also reshape the records.
type mutation struct {
	id     string
	suite  string
	clause string
	expect string
	note   string

	leaves   func([]leaf) []leaf
	records  func([]record) []record
	rawSnap  func([]byte) []byte
	rawPre   func([]byte) []byte
	keepRoot bool // keep the valid root in the header, so check 1 catches it
	verbatim bool // write records in the order given, not keccak order
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
		var snap, pre []byte // nil means untouched; empty means an empty file
		switch {
		case m.leaves != nil:
			leaves := m.leaves(cloneLeaves(valid.leaves))
			root := valid.root
			if !m.keepRoot {
				root = foldRoot(leaves)
			}
			snap = encodeSnapshot(root, uint64(len(leaves)), leaves)
			if m.records != nil {
				pre = encodePreimages(m.records(cloneRecords(valid.records)))
			}
		case m.records != nil:
			recs := m.records(cloneRecords(valid.records))
			if m.verbatim {
				pre = encodeRecordsVerbatim(recs)
			} else {
				pre = encodePreimages(recs)
			}
		case m.rawSnap != nil:
			snap = m.rawSnap(mustRead(valid.snapshotFD))
		case m.rawPre != nil:
			pre = m.rawPre(mustRead(valid.preimageFD))
		default:
			return nil, fmt.Errorf("case %s shapes nothing", m.id)
		}
		changed := false
		for _, f := range []struct {
			blob  []byte
			valid string
			name  string
			field *string
		}{
			{snap, valid.snapshotFD, "snapshot.bin", &entry.Snapshot},
			{pre, valid.preimageFD, "preimages.bin", &entry.Preimages},
		} {
			if f.blob == nil {
				continue
			}
			path := filepath.Join(dir, f.name)
			if err := os.WriteFile(path, f.blob, 0644); err != nil {
				return nil, err
			}
			*f.field = rel(outDir, path)
			changed = changed || !bytes.Equal(f.blob, mustRead(f.valid))
		}
		if !changed {
			return nil, fmt.Errorf("case %s produced the valid files unchanged", m.id)
		}
		entry.Effect = &effect{}
		if snap != nil {
			entry.Effect.Snapshot = snapshotEffect(mustRead(valid.snapshotFD), snap)
		}
		if pre != nil {
			entry.Effect.Preimages = preimageEffect(mustRead(valid.preimageFD), pre)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// produceCases each delete one preimage from the converter's source store,
// which converter step 2 must refuse. The shell has no keccak, so the store
// keys are recorded here. Deleting a slot's preimage removes it for every
// account holding that slot, so the slot target has one holder.
func produceCases(alloc types.GenesisAlloc) ([]caseEntry, error) {
	slot := h(7)
	var holders int
	for _, acct := range alloc {
		if _, ok := acct.Storage[slot]; ok {
			holders++
		}
	}
	if holders != 1 {
		return nil, fmt.Errorf("slot 7 is held by %d accounts; the missing-slot case needs one", holders)
	}
	drop := func(id, note string, preimage []byte) caseEntry {
		return caseEntry{
			ID: "produce/" + id, Suite: "produce", Expect: "reject",
			Clause: "converter.preimage-set-matches-leaves", Note: note,
			Defect: []string{"drop-preimage", crypto.Keccak256Hash(preimage).Hex()},
		}
	}
	return []caseEntry{
		drop("missing-account-preimage", "the source has no preimage for account "+eoaBalance.Hex(), eoaBalance[:]),
		drop("missing-slot-preimage", "the source has no preimage for slot 7, held by "+storageHeader.Hex(), slot[:]),
	}, nil
}

func mustRead(path string) []byte {
	blob, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return blob
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
	ID        string   `json:"id"`
	Suite     string   `json:"suite"`
	Snapshot  string   `json:"snapshot,omitempty"`
	Preimages string   `json:"preimages,omitempty"`
	Defect    []string `json:"defect,omitempty"`
	Expect    string   `json:"expect"`
	Clause    string   `json:"clause"`
	Note      string   `json:"note,omitempty"`
	Effect    *effect  `json:"effect,omitempty"`
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
		Gates          *gates `json:"gates"`
	} `json:"valid"`
	Cases []caseEntry `json:"cases"`
}

func writeManifest(outDir, genesisPath string, valid *artifacts, g *gates, cases []caseEntry) error {
	if a, b := duplicateEffect(cases); a != "" {
		return fmt.Errorf("cases %s and %s record the same effect: they are one mutation written twice", a, b)
	}
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
	m.Valid.Gates = g
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

func encodeSnapshot(root common.Hash, count uint64, leaves []leaf) []byte {
	var buf bytes.Buffer
	buf.Write(root[:])
	buf.Write(binary.BigEndian.AppendUint64(nil, count))
	for _, l := range leaves {
		buf.Write(encodeLeaf(l))
	}
	return buf.Bytes()
}

// encodeRecordsVerbatim writes records in the order given.
func encodeRecordsVerbatim(recs []record) []byte {
	var buf bytes.Buffer
	for _, r := range recs {
		buf.Write(r.addr[:])
		buf.Write(binary.BigEndian.AppendUint32(nil, uint32(len(r.slots))))
		for _, s := range r.slots {
			buf.Write(s[:])
		}
	}
	return buf.Bytes()
}

// encodePreimages writes records in keccak order.
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
