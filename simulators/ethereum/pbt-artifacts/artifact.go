package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// testCase is one fixture. A verify case names an artifact pair and the
// effect its mutation had; a produce case names the defect a shim applies to
// the converter's source instead.
type testCase struct {
	ID        string   `json:"id"`
	Suite     string   `json:"suite"`
	Snapshot  string   `json:"snapshot"`
	Preimages string   `json:"preimages"`
	Defect    []string `json:"defect"`
	Expect    string   `json:"expect"`
	Clause    string   `json:"clause"`
	Note      string   `json:"note,omitempty"`
	Effect    *effect  `json:"effect"`
}

const (
	expectReject      = "reject"
	expectUnspecified = "unspecified"
)

// effect mirrors what fixtures/gen recorded for a case, and is what the
// case is scored on. The generator is its own module, so the shape is
// declared in both and the simulator only reads it.
type effect struct {
	Snapshot  *fileEffect `json:"snapshot,omitempty"`
	Preimages *fileEffect `json:"preimages,omitempty"`
}

type fileEffect struct {
	ValidLen  int      `json:"validLen"`
	CaseLen   int      `json:"caseLen"`
	FirstDiff int      `json:"firstDiff"`
	Parses    bool     `json:"parses"`
	Added     []string `json:"added,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Changed   []string `json:"changed,omitempty"`
	Reordered bool     `json:"reordered,omitempty"`
	RootKept  bool     `json:"rootKept,omitempty"`
	Claimed   uint64   `json:"claimed,omitempty"`
}

func (f *fileEffect) String() string {
	s := fmt.Sprintf("%d -> %d bytes, first differs at %d", f.ValidLen, f.CaseLen, f.FirstDiff)
	if !f.Parses {
		s += ", no longer parses"
	}
	for _, l := range []struct {
		label string
		list  []string
	}{{"added", f.Added}, {"removed", f.Removed}, {"changed", f.Changed}} {
		if len(l.list) > 0 {
			s += "; " + l.label + " " + strings.Join(l.list, " ")
		}
	}
	if f.Reordered {
		s += "; reordered"
	}
	if f.RootKept {
		s += "; root kept"
	}
	if f.Claimed != 0 {
		s += fmt.Sprintf("; header claims %d", f.Claimed)
	}
	return s
}

// describe is the test's description in the results: the clause, and what
// the case did to the bytes or to the converter's source.
func (tc testCase) describe() string {
	s := fmt.Sprintf("Clause: %s (%s)", tc.Clause, tc.Expect)
	if tc.Note != "" {
		s += "\n" + tc.Note
	}
	if len(tc.Defect) > 0 {
		s += "\ndefect: " + strings.Join(tc.Defect, " ")
	}
	if tc.Effect == nil {
		return s
	}
	if f := tc.Effect.Snapshot; f != nil {
		s += "\nsnapshot: " + f.String()
	}
	if f := tc.Effect.Preimages; f != nil {
		s += "\npreimages: " + f.String()
	}
	return s
}

// loadManifest reads the fixture set, hashes every file it names, checks
// the valid pair against the digests the manifest claims and refuses a set
// that cannot be scored structurally: a case with no effect, or two cases
// recording the same one, would score clients against nothing reproducible.
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
	byEffect := map[string]string{}
	for _, c := range m.Cases {
		if c.Expect != expectReject && c.Expect != expectUnspecified {
			return nil, fmt.Errorf("case %s expects %q", c.ID, c.Expect)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("case %s appears twice", c.ID)
		}
		seen[c.ID] = true
		if c.Suite == "produce" {
			if len(c.Defect) == 0 || c.Snapshot != "" || c.Preimages != "" {
				return nil, fmt.Errorf("produce case %s must name a defect and no artifact", c.ID)
			}
			key := "defect " + strings.Join(c.Defect, " ")
			if prev, dup := byEffect[key]; dup {
				return nil, fmt.Errorf("cases %s and %s apply the same defect", prev, c.ID)
			}
			byEffect[key] = c.ID
			continue
		}
		if c.Suite != "preimages" && c.Suite != "snapshot" {
			return nil, fmt.Errorf("case %s is in suite %q", c.ID, c.Suite)
		}
		if c.Effect == nil || (c.Effect.Snapshot == nil && c.Effect.Preimages == nil) {
			return nil, fmt.Errorf("case %s records no effect: regenerate the fixtures", c.ID)
		}
		key, _ := json.Marshal(c.Effect)
		if prev, dup := byEffect[string(key)]; dup {
			return nil, fmt.Errorf("cases %s and %s record the same effect: they are the same fixture twice", prev, c.ID)
		}
		byEffect[string(key)] = c.ID
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
