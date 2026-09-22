package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ethereum/go-ethereum/common"
)

// effect is what a case changed against the valid pair, computed from the
// bytes rather than described. The generator knows what it mutated, so the
// fixture set records it instead of asking a client to say it in prose.
type effect struct {
	Snapshot  *fileEffect `json:"snapshot,omitempty"`
	Preimages *fileEffect `json:"preimages,omitempty"`
}

// fileEffect is one artifact's change. Added, Removed and Changed name
// snapshot leaf keys or preimage record addresses, at most four each, in
// file order.
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
	Claimed   int      `json:"claimed,omitempty"`
}

// snapshotEffect diffs a case's snapshot against the valid one. The valid
// blob must decode: the generator has just written it.
func snapshotEffect(validBlob, caseBlob []byte, validRoot common.Hash) *fileEffect {
	f := &fileEffect{
		ValidLen:  len(validBlob),
		CaseLen:   len(caseBlob),
		FirstDiff: firstDiff(validBlob, caseBlob),
	}
	_, _, validLeaves, err := decodeSnapshotLoose(validBlob)
	if err != nil {
		panic(fmt.Sprintf("the valid snapshot does not decode: %v", err))
	}
	root, claimed, caseLeaves, err := decodeSnapshotLoose(caseBlob)
	f.Parses = err == nil
	f.Added, f.Removed, f.Changed, f.Reordered = diffEntries(leafEntries(validLeaves), leafEntries(caseLeaves), "=")
	if uint64(len(caseLeaves)) != claimed {
		f.Claimed = int(claimed)
	}
	if root == validRoot && f.touchesLeaves() {
		f.RootKept = true
	}
	return f
}

// preimageEffect diffs a case's preimage file against the valid one. It has
// no header, so no root is kept and no count is claimed.
func preimageEffect(validBlob, caseBlob []byte) *fileEffect {
	f := &fileEffect{
		ValidLen:  len(validBlob),
		CaseLen:   len(caseBlob),
		FirstDiff: firstDiff(validBlob, caseBlob),
	}
	validRecs, err := decodePreimagesLoose(validBlob)
	if err != nil {
		panic(fmt.Sprintf("the valid preimage file does not decode: %v", err))
	}
	caseRecs, err := decodePreimagesLoose(caseBlob)
	f.Parses = err == nil
	f.Added, f.Removed, f.Changed, f.Reordered = diffEntries(recordEntries(validRecs), recordEntries(caseRecs), ":")
	return f
}

func (f *fileEffect) touchesLeaves() bool {
	return len(f.Added) > 0 || len(f.Removed) > 0 || len(f.Changed) > 0 || f.Reordered
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

// slotDigest folds a record's slot keys, so a slot added, removed, replaced
// or reordered inside it yields a different token.
func slotDigest(r record) string {
	var buf bytes.Buffer
	for _, s := range r.slots {
		buf.Write(s[:])
	}
	return hex.EncodeToString(sha256sum(buf.Bytes()))[:16]
}

// diffEntries names what the case added, removed and changed, each list held
// to the first four in file order, and whether the entries they share moved.
func diffEntries(valid, cs []entry, sep string) (added, removed, changed []string, reordered bool) {
	validVal := values(valid)
	caseVal := values(cs)
	for _, e := range cs {
		switch v, ok := validVal[e.name]; {
		case !ok:
			added = appendCapped(added, e.name+sep+e.value)
		case v != e.value:
			changed = appendCapped(changed, e.name+sep+e.value)
		}
	}
	for _, e := range valid {
		if _, ok := caseVal[e.name]; !ok {
			removed = appendCapped(removed, e.name+sep+e.value)
		}
	}
	return added, removed, changed, !slices.Equal(shared(cs, validVal), shared(valid, caseVal))
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

func appendCapped(list []string, s string) []string {
	if len(list) >= 4 {
		return list
	}
	return append(list, s)
}

func firstDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// distinctEffects counts the effects no other case shares, which is what the
// simulator requires of the set it scores.
func distinctEffects(cases []caseEntry) int {
	seen := map[string]bool{}
	for _, c := range cases {
		key, err := json.Marshal(c.Effect)
		if err != nil {
			panic(err)
		}
		seen[string(key)] = true
	}
	return len(seen)
}
