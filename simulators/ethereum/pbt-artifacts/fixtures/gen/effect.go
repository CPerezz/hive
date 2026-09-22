package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ethereum/go-ethereum/common"
)

// effect is what a case changed against the valid pair, computed from the
// bytes the generator wrote; README.md says why the fixtures carry it.
type effect struct {
	Snapshot  *fileEffect `json:"snapshot,omitempty"`
	Preimages *fileEffect `json:"preimages,omitempty"`
}

// fileEffect is one artifact's change. Added, Removed and Changed name
// snapshot leaf keys or preimage record addresses, at most four each, in
// file order. RootKept is a header still claiming the valid root over
// contents that moved; Claimed is its leaf count, written only when it
// disagrees with the leaves the file holds.
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

// snapshotEffect diffs a case's snapshot against the valid one, which must
// decode: the generator has just written it.
func snapshotEffect(validBlob, caseBlob []byte) *fileEffect {
	f := &fileEffect{
		ValidLen:  len(validBlob),
		CaseLen:   len(caseBlob),
		FirstDiff: firstDiff(validBlob, caseBlob),
	}
	validRoot, _, validLeaves, err := decodeSnapshotLoose(validBlob)
	if err != nil {
		panic(fmt.Sprintf("the valid snapshot does not decode: %v", err))
	}
	root, claimed, caseLeaves, err := decodeSnapshotLoose(caseBlob)
	f.Parses = err == nil
	f.Added, f.Removed, f.Changed, f.Reordered = diffEntries(leafEntries(validLeaves), leafEntries(caseLeaves), "=")
	if uint64(len(caseLeaves)) != claimed {
		f.Claimed = claimed
	}
	f.RootKept = root == validRoot && (len(f.Added)+len(f.Removed)+len(f.Changed) > 0 || f.Reordered)
	return f
}

// preimageEffect diffs a case's preimage file against the valid one. The
// file has no header, so no root is kept and no count is claimed.
func preimageEffect(validBlob, caseBlob []byte) *fileEffect {
	f := &fileEffect{
		ValidLen:  len(validBlob),
		CaseLen:   len(caseBlob),
		FirstDiff: firstDiff(validBlob, caseBlob),
	}
	validRecs, err := decodePreimages(validBlob)
	if err != nil {
		panic(fmt.Sprintf("the valid preimage file does not decode: %v", err))
	}
	caseRecs, err := decodePreimages(caseBlob)
	f.Parses = err == nil
	f.Added, f.Removed, f.Changed, f.Reordered = diffEntries(recordEntries(validRecs), recordEntries(caseRecs), ":")
	return f
}

// entry is one file element reduced to what the diff needs: a name that is
// its identity, and a value token that changes whenever its content does.
type entry struct{ name, value string }

func leafEntries(leaves []leaf) []entry {
	out := make([]entry, len(leaves))
	for i, l := range leaves {
		out[i] = entry{
			name:  hex.EncodeToString(l.key),
			value: hex.EncodeToString(common.TrimLeftZeroes(l.value[:])),
		}
	}
	return out
}

func recordEntries(recs []record) []entry {
	out := make([]entry, len(recs))
	for i, r := range recs {
		out[i] = entry{name: hex.EncodeToString(r.addr[:]), value: slotDigest(r)}
	}
	return out
}

// slotDigest folds a record's slot keys into the first 64 bits of their
// hash, so a slot added, removed, replaced or reordered yields a different
// token. 64 bits is ample to separate the records of one fixture set.
func slotDigest(r record) string {
	var keys []byte
	for _, s := range r.slots {
		keys = append(keys, s[:]...)
	}
	return hex.EncodeToString(sha256sum(keys))[:16]
}

// diffEntries names what the case added, removed and changed, each list held
// to the first four in file order, and whether the entries they share moved.
func diffEntries(valid, cs []entry, sep string) (added, removed, changed []string, reordered bool) {
	validVal := values(valid)
	caseVal := values(cs)
	for _, e := range cs {
		switch v, ok := validVal[e.name]; {
		case !ok:
			added = append(added, e.name+sep+e.value)
		case v != e.value:
			changed = append(changed, e.name+sep+e.value)
		}
	}
	for _, e := range valid {
		if _, ok := caseVal[e.name]; !ok {
			removed = append(removed, e.name+sep+e.value)
		}
	}
	first4 := func(l []string) []string { return l[:min(len(l), 4)] }
	return first4(added), first4(removed), first4(changed),
		!slices.Equal(shared(cs, validVal), shared(valid, caseVal))
}

func values(entries []entry) map[string]string {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		out[e.name] = e.value
	}
	return out
}

// shared is the entry names in file order, filtered to those the other file
// also holds, which is the sequence a reordering disturbs.
func shared(entries []entry, other map[string]string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if _, ok := other[e.name]; ok {
			out = append(out, e.name)
		}
	}
	return out
}

func firstDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// duplicateEffect names the first two cases recording the same effect: they
// are one mutation written twice, and the simulator refuses such a set.
func duplicateEffect(cases []caseEntry) (string, string) {
	seen := make(map[string]string, len(cases))
	for _, c := range cases {
		key, _ := json.Marshal(c.Effect)
		if prev, dup := seen[string(key)]; dup {
			return prev, c.ID
		}
		seen[string(key)] = c.ID
	}
	return "", ""
}
