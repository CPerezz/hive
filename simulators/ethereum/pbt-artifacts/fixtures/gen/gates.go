package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// pbtRootPy is the spec reference's root for a genesis; see the script.
//
//go:embed pbt_root.py
var pbtRootPy string

// gates records the admission checks a canonical pair passed. Neither rests
// on geth's PBT code: the strict decoder is this generator's own, and the
// root comes from execution-specs.
type gates struct {
	StrictDecoder string `json:"strictDecoder"`
	SpecRoot      string `json:"specRoot"`
	SpecReference string `json:"specReference,omitempty"`
}

// admit runs the root gate on a pair convert has already held to the strict
// decoder. Without a reference checkout the root gate is skipped.
func admit(valid *artifacts, genesisPath, ref string) (*gates, error) {
	g := &gates{StrictDecoder: "pass", SpecRoot: "skipped"}
	if ref == "" {
		return g, nil
	}
	root, commit, err := specRoot(ref, genesisPath)
	if err != nil {
		return nil, err
	}
	if root != valid.root {
		return nil, fmt.Errorf("%s: the spec reference roots it at %x, the converter at %x", genesisPath, root, valid.root)
	}
	g.SpecRoot, g.SpecReference = "pass", "execution-specs@"+commit
	return g, nil
}

// specRoot runs pbt_root.py on a genesis with the checkout's own interpreter.
func specRoot(ref, genesisPath string) (common.Hash, string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command(filepath.Join(ref, ".venv", "bin", "python"), "-", genesisPath)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(ref, "src"))
	cmd.Stdin = strings.NewReader(pbtRootPy)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return common.Hash{}, "", fmt.Errorf("spec reference on %s: %w\n%s", genesisPath, err, stderr.String())
	}
	commit, err := exec.Command("git", "-C", ref, "rev-parse", "--short=9", "HEAD").Output()
	if err != nil {
		return common.Hash{}, "", fmt.Errorf("spec reference commit: %w", err)
	}
	return common.HexToHash(strings.TrimSpace(string(out))), strings.TrimSpace(string(commit)), nil
}

// check re-runs the gates on the checked-in canonical pair and writes
// nothing; fixtures/validate.sh calls it.
func check(outDir, ref string) error {
	var m manifest
	if err := json.Unmarshal(mustRead(filepath.Join(outDir, "manifest.json")), &m); err != nil {
		return fmt.Errorf("manifest.json: %w", err)
	}
	snap := mustRead(filepath.Join(outDir, m.Valid.Snapshot))
	if err := decodeSnapshotStrict(snap); err != nil {
		return fmt.Errorf("%s: %w", m.Valid.Snapshot, err)
	}
	if ref != "" {
		root, _, err := specRoot(ref, filepath.Join(outDir, m.Genesis.File))
		if err != nil {
			return err
		}
		if claimed := common.BytesToHash(snap[:32]); root != claimed {
			return fmt.Errorf("the spec reference roots the fixture at %x, the snapshot claims %x", root, claimed)
		}
	}
	fmt.Println("gates hold")
	return nil
}
