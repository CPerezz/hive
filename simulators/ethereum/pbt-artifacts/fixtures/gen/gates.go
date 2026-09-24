package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// pbtRootPy is the spec reference's root for a genesis; see the script.
//
//go:embed pbt_root.py
var pbtRootPy string

// The canonical pair as admitted: execution-specs computes its root, and
// nethermind writes the same bytes. The image regenerates it on every build,
// so a converter or generator change that moves it fails there. To move it
// on purpose, admit the new pair with validate.sh and an execution-specs
// checkout, then update both.
var (
	pinnedSnapshot  = common.HexToHash("0xf2301b3bf78f12445f102e1761824c4c05a46b9cc5153cc415abef42626258db")
	pinnedPreimages = common.HexToHash("0xe0af5df37c748df3eb3ba0adb07b138e19ffaeccfd6e2eaf7c46ebaf7dc60d2b")
)

// admit holds a pair convert has already held to the strict decoder to the
// spec reference's root, when a checkout is given, and to the pin. Neither
// rests on geth's PBT code.
func admit(valid *artifacts, genesisPath, ref string) error {
	if ref != "" {
		root, err := specRoot(ref, genesisPath)
		if err != nil {
			return err
		}
		if root != valid.root {
			return fmt.Errorf("the spec reference roots the genesis at %x, the converter at %x", root, valid.root)
		}
	}
	snap := crypto.Keccak256Hash(mustRead(valid.snapshotFD))
	pre := crypto.Keccak256Hash(mustRead(valid.preimageFD))
	if snap != pinnedSnapshot || pre != pinnedPreimages {
		return fmt.Errorf("the canonical pair moved: snapshot %x, preimages %x; pinned %x, %x", snap, pre, pinnedSnapshot, pinnedPreimages)
	}
	return nil
}

// specRoot runs pbt_root.py on a genesis with the checkout's own interpreter.
func specRoot(ref, genesisPath string) (common.Hash, error) {
	var stderr bytes.Buffer
	cmd := exec.Command(filepath.Join(ref, ".venv", "bin", "python"), "-", genesisPath)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(ref, "src"))
	cmd.Stdin = strings.NewReader(pbtRootPy)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return common.Hash{}, fmt.Errorf("spec reference on %s: %w\n%s", genesisPath, err, stderr.String())
	}
	return common.HexToHash(strings.TrimSpace(string(out))), nil
}
