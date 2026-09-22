package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// The fixture manifest, as written by fixtures/gen. The simulator reads the
// artifacts as opaque bytes: what they encode is what the clients are being
// measured on, so nothing here re-implements the EIP's rules.
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
	Cases []testCase `json:"cases"`

	dir string
}

// testCase is one fixture: a pair of artifact paths and the outcome a
// conforming client must produce for them.
type testCase struct {
	ID        string `json:"id"`
	Suite     string `json:"suite"`
	Snapshot  string `json:"snapshot"`
	Preimages string `json:"preimages"`
	Expect    string `json:"expect"`
	Clause    string `json:"clause"`
	Note      string `json:"note,omitempty"`
}

const (
	expectAccept      = "accept"
	expectReject      = "reject"
	expectUnspecified = "unspecified"
)

// loadManifest reads the fixture set and checks it against itself: the valid
// artifacts must hash to the digests the manifest names, and every case file
// must exist. A fixture set that fails this would score clients against
// artifacts nobody can reproduce, so it is a fatal error rather than a test
// failure.
func loadManifest(dir string) (*manifest, error) {
	blob, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, fmt.Errorf("manifest does not parse: %w", err)
	}
	m.dir = dir

	for _, f := range []struct{ path, want string }{
		{m.Valid.Snapshot, m.Valid.SnapshotDigest},
		{m.Valid.Preimages, m.Valid.PreimageDigest},
	} {
		blob, err := os.ReadFile(filepath.Join(dir, f.path))
		if err != nil {
			return nil, err
		}
		if got := crypto.Keccak256Hash(blob); got != common.HexToHash(f.want) {
			return nil, fmt.Errorf("%s hashes to %s, the manifest names %s", f.path, got.Hex(), f.want)
		}
	}
	for _, c := range m.Cases {
		switch c.Expect {
		case expectReject, expectUnspecified:
		default:
			return nil, fmt.Errorf("case %s expects %q, which is not a case outcome", c.ID, c.Expect)
		}
		for _, p := range []string{c.Snapshot, c.Preimages} {
			if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
				return nil, fmt.Errorf("case %s: %w", c.ID, err)
			}
		}
	}
	return &m, nil
}

// cases returns the fixtures of one suite.
func (m *manifest) cases(suite string) []testCase {
	var out []testCase
	for _, c := range m.Cases {
		if c.Suite == suite {
			out = append(out, c)
		}
	}
	return out
}

// digest is the keccak256 of a fixture file, named the way EIP-8347 names
// its two canonical digests. An unreadable file yields the zero hash, which
// the caller will fail on for a better reason a moment later.
func (m *manifest) digest(path string) string {
	blob, err := os.ReadFile(filepath.Join(m.dir, path))
	if err != nil {
		return common.Hash{}.Hex()
	}
	return crypto.Keccak256Hash(blob).Hex()
}

// read returns a fixture file's bytes.
func (m *manifest) read(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(m.dir, path))
}
