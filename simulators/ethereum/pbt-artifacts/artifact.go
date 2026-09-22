package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// manifest is the fixture set as fixtures/gen writes it. The artifacts are
// opaque bytes here: what they encode is what the clients are measured on.
type manifest struct {
	Genesis struct {
		File      string `json:"file"`
		StateRoot string `json:"stateRoot"`
	} `json:"genesis"`
	Valid struct {
		Snapshot       string `json:"snapshot"`
		Preimages      string `json:"preimages"`
		SnapshotDigest string `json:"snapshotDigest"`
		PreimageDigest string `json:"preimageDigest"`
	} `json:"valid"`
	Cases []testCase `json:"cases"`

	dir     string
	digests map[string]string
}

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
	expectReject      = "reject"
	expectUnspecified = "unspecified"
)

func (tc testCase) describe() string {
	s := fmt.Sprintf("Clause: %s (%s)", tc.Clause, tc.Expect)
	if tc.Note != "" {
		s += "\n" + tc.Note
	}
	return s
}

// loadManifest reads the fixture set, hashes every file it names and checks
// the valid pair against the digests the manifest claims. A set failing this
// would score clients against bytes nobody can reproduce.
func loadManifest(dir string) (*manifest, error) {
	blob, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	m := &manifest{dir: dir, digests: map[string]string{}}
	if err := json.Unmarshal(blob, m); err != nil {
		return nil, fmt.Errorf("manifest does not parse: %w", err)
	}
	seen := map[string]bool{}
	for _, c := range m.Cases {
		if c.Expect != expectReject && c.Expect != expectUnspecified {
			return nil, fmt.Errorf("case %s expects %q", c.ID, c.Expect)
		}
		if c.Suite != "preimages" && c.Suite != "snapshot" {
			return nil, fmt.Errorf("case %s is in suite %q", c.ID, c.Suite)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("case %s appears twice", c.ID)
		}
		seen[c.ID] = true
		for _, p := range []string{c.Snapshot, c.Preimages} {
			if err := m.hash(p); err != nil {
				return nil, fmt.Errorf("case %s: %w", c.ID, err)
			}
		}
	}
	for _, f := range []struct{ path, want string }{
		{m.Valid.Snapshot, m.Valid.SnapshotDigest},
		{m.Valid.Preimages, m.Valid.PreimageDigest},
	} {
		if err := m.hash(f.path); err != nil {
			return nil, err
		}
		if got := m.digests[f.path]; got != common.HexToHash(f.want).Hex() {
			return nil, fmt.Errorf("%s hashes to %s, the manifest names %s", f.path, got, f.want)
		}
	}
	return m, nil
}

func (m *manifest) hash(path string) error {
	if _, ok := m.digests[path]; ok {
		return nil
	}
	blob, err := m.read(path)
	if err != nil {
		return err
	}
	m.digests[path] = crypto.Keccak256Hash(blob).Hex()
	return nil
}

func (m *manifest) read(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(m.dir, path))
}

func (m *manifest) digest(path string) string { return m.digests[path] }

func (m *manifest) cases(suite string) []testCase {
	var out []testCase
	for _, c := range m.Cases {
		if c.Suite == suite {
			out = append(out, c)
		}
	}
	return out
}

func (m *manifest) validFile(artifact string) string {
	if artifact == "snapshot" {
		return m.Valid.Snapshot
	}
	return m.Valid.Preimages
}
