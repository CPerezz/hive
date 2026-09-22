// The pbt-artifacts simulator measures how each client handles the EIP-8347
// offline migration artifacts: the preimage file and the PBT snapshot.
//
// Every client is driven through one shim script, uploaded into its container
// at /hive-bin/pbt-artifacts.sh, which translates four verbs into whatever
// that client's command line happens to be. No client's own hive files are
// touched, so adding a client is one new script in shims/.
//
// The shim's contract, and the whole of the coupling between this simulator
// and a client:
//
//	genesis-root                              -> stdout mpt_root=0x...
//	verify  <snapshot> <preimages> <anchor>   -> stdout root=0x...
//	convert <anchor>                          -> stdout root=0x... snapshot=<base64> preimages=<base64>
//	import  <snapshot> <preimages> <anchor>   -> stdout root=0x...
//
// Exit status 0 means the client accepted the artifacts, 1 that it rejected
// them cleanly, 3 that the client cannot do this at all, and anything else is
// a crash. A crash is never counted as a rejection.
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/ethereum/hive/hivesim"
)

const (
	// fixtureDir holds the artifacts; fixtureTar is the same tree as one file,
	// which is what actually gets uploaded, because a client container takes
	// its files at start and one upload beats ninety.
	fixtureDir = "fixtures"
	fixtureTar = "fixtures.tar"

	// anchor is the block the artifacts are anchored at. Genesis needs no
	// chain import, and every client can name it.
	anchor = "0"

	// Shim exit codes.
	exitAccept      = 0
	exitReject      = 1
	exitUnsupported = 3
)

var (
	rootRE      = regexp.MustCompile(`(?m)^root=(0x[0-9a-fA-F]{64})`)
	mptRootRE   = regexp.MustCompile(`(?m)^mpt_root=(0x[0-9a-fA-F]{64})`)
	snapshotRE  = regexp.MustCompile(`(?m)^snapshot=([A-Za-z0-9+/=]+)`)
	preimagesRE = regexp.MustCompile(`(?m)^preimages=([A-Za-z0-9+/=]+)`)
)

func main() {
	fixtures, err := loadManifest(fixtureDir)
	if err != nil {
		panic(fmt.Sprintf("fixture set is unusable: %v", err))
	}
	sim := hivesim.New()
	suite := hivesim.Suite{
		Name: "pbt-artifacts",
		Description: `Conformance of the EIP-8347 offline migration artifacts.

Each client is handed the same byte-canonical preimage file and PBT snapshot
and must accept the sound pair, reject every unsound one, and reproduce both
files when it converts the anchor state itself. Clients that cannot do a verb
report it as unsupported rather than failing case by case.`,
	}
	suite.Add(hivesim.TestSpec{
		Name:        "artifact conformance",
		Description: "Runs every fixture against every client that hive was given.",
		Run:         func(t *hivesim.T) { runAllClients(t, fixtures) },
	})
	hivesim.MustRunSuite(sim, suite)
}

// runAllClients fans out over the client types hive was started with. Each
// client gets its own container and its own set of result rows.
func runAllClients(t *hivesim.T, fixtures *manifest) {
	clients, err := t.Sim.ClientTypes()
	if err != nil {
		t.Fatalf("cannot list client types: %v", err)
	}
	producers := newProducerSet()
	var wg sync.WaitGroup
	for _, ct := range clients {
		wg.Add(1)
		go func(ct *hivesim.ClientDefinition) {
			defer wg.Done()
			t.Run(hivesim.TestSpec{
				Name:        fmt.Sprintf("%s: artifacts", ct.Name),
				Description: "Every fixture, against one client.",
				Run:         func(t *hivesim.T) { runClient(t, fixtures, ct, producers) },
			})
		}(ct)
	}
	wg.Wait()

	// Agreement is a property of the producers as a set, so it can only be
	// judged once every client has had its turn.
	runAgreement(t, fixtures, producers)
}

// client is one container plus the shim that drives it. Execs are serialized:
// the verbs share a datadir inside the container, and two at once would race
// for its lock.
type client struct {
	*hivesim.Client
	mu sync.Mutex
}

func (c *client) run(verb string, args ...string) *hivesim.ExecInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := c.Exec(append([]string{"pbt-artifacts.sh", verb}, args...)...)
	if err != nil {
		// A transport failure is not the client's answer, so it is reported
		// as the crash it is rather than folded into a rejection.
		return &hivesim.ExecInfo{ExitCode: -1, Stderr: err.Error()}
	}
	return info
}

// verify runs the verify verb, handing the shim the artifacts' keccak
// digests as well. A client whose consumption path wants them (nethermind
// wants both, inside a manifest EIP-8347 does not define) would otherwise
// have to hash the files itself, and a shim has no keccak; worse, a shim
// filling them in with something arbitrary would make the client reject the
// digest rather than the clause the case is about.
func (c *client) verify(fixtures *manifest, snapshot, preimages string) *hivesim.ExecInfo {
	return c.run("verify", snapshot, preimages, anchor,
		fixtures.digest(snapshot), fixtures.digest(preimages))
}

func runClient(t *hivesim.T, fixtures *manifest, ct *hivesim.ClientDefinition, producers *producerSet) {
	files := map[string]string{
		"/genesis.json":              filepath.Join(fixtureDir, fixtures.Genesis.File),
		"/pbt-fixtures.tar":          fixtureTar,
		"/hive-bin/pbt-artifacts.sh": shimFor(ct.Name),
	}
	c := &client{Client: t.StartClient(ct.Name,
		// The verbs run offline against a datadir of the shim's own, so the
		// node's RPC never has to come up for this simulator to work.
		hivesim.Params{"HIVE_CHECK_LIVE_PORT": "0", "HIVE_LOGLEVEL": "3"},
		hivesim.WithStaticFiles(files),
	)}

	report := newReport(ct.Name, c.Type)
	defer report.publish(t)

	// The anchor check gates everything else: a client that does not agree on
	// the state being converted cannot be measured against artifacts of it.
	anchored := runGenesisRoot(t, c, fixtures, report)

	for _, suite := range []string{"preimages", "snapshot"} {
		runVerifySuite(t, c, fixtures, report, suite, anchored)
	}
	runConvert(t, c, fixtures, report, producers, anchored)
}

// runGenesisRoot checks that the client built the same anchor state the
// fixtures were derived from.
func runGenesisRoot(t *hivesim.T, c *client, fixtures *manifest, report *report) bool {
	var ok bool
	t.Run(hivesim.TestSpec{
		Name:        fmt.Sprintf("%s/genesis/state-root", c.Type),
		Description: "The client's block-0 state root must be the one the artifacts are anchored to.",
		Run: func(t *hivesim.T) {
			info := c.run("genesis-root")
			switch info.ExitCode {
			case exitUnsupported:
				report.set("genesis_root", "unsupported")
				t.Fatalf("unsupported: the shim cannot read this client's genesis state root\n%s", info.Stderr)
			case exitAccept:
			default:
				report.set("genesis_root", "crash")
				t.Fatalf("crash: genesis-root exited %d\n%s", info.ExitCode, info.Stderr)
			}
			got := match(mptRootRE, info.Stdout)
			if got == "" {
				report.set("genesis_root", "crash")
				t.Fatalf("crash: genesis-root printed no mpt_root=\n%s%s", info.Stdout, info.Stderr)
			}
			if !strings.EqualFold(got, fixtures.Genesis.StateRoot) {
				report.set("genesis_root", "mismatch")
				t.Fatalf("the client's anchor state root is %s, the fixtures are anchored at %s",
					got, fixtures.Genesis.StateRoot)
			}
			report.set("genesis_root", "ok")
			ok = true
		},
	})
	return ok
}

// runVerifySuite runs one suite's fixtures: the valid pair first, because a
// client that rejects it cannot be judged on what it rejects afterwards.
func runVerifySuite(t *hivesim.T, c *client, fixtures *manifest, report *report, suite string, anchored bool) {
	cases := fixtures.cases(suite)
	key := "verify_" + suite

	if !anchored {
		report.set(key, "inconclusive")
		skipAll(t, c.Type, suite, cases, "inconclusive: the client's anchor state root does not match the fixtures")
		return
	}

	// The baseline: the sound pair, which every conforming client accepts.
	var baseline int
	t.Run(hivesim.TestSpec{
		Name:        fmt.Sprintf("%s/%s/valid", c.Type, suite),
		Description: "The sound artifact pair must be accepted and its PBT root reported.",
		Run: func(t *hivesim.T) {
			info := c.verify(fixtures, fixtures.Valid.Snapshot, fixtures.Valid.Preimages)
			baseline = info.ExitCode
			switch info.ExitCode {
			case exitUnsupported:
				t.Fatalf("unsupported: this client cannot verify artifacts\n%s", info.Stderr)
			case exitAccept:
				if got := match(rootRE, info.Stdout); got != "" && !strings.EqualFold(got, fixtures.Genesis.PBTRoot) {
					t.Fatalf("accepted the artifacts but reports PBT root %s, the fixtures claim %s", got, fixtures.Genesis.PBTRoot)
				}
			case exitReject:
				t.Fatalf("the sound artifact pair was rejected\n%s", info.Stderr)
			default:
				t.Fatalf("crash: verify exited %d\n%s", info.ExitCode, info.Stderr)
			}
		},
	})
	switch baseline {
	case exitAccept:
	case exitUnsupported:
		// One row saying "this client cannot do it" beats N identical rows.
		report.set(key, "unsupported")
		skipAll(t, c.Type, suite, cases, "unsupported: this client cannot verify artifacts")
		return
	default:
		report.set(key, "inconclusive")
		skipAll(t, c.Type, suite, cases, "inconclusive: the client rejected the sound pair, so its rejections say nothing")
		return
	}

	var scored, passed int
	for _, tc := range cases {
		t.Run(hivesim.TestSpec{
			Name:        fmt.Sprintf("%s/%s", c.Type, tc.ID),
			Description: fmt.Sprintf("%s\n\nClause: %s\nExpected: %s", tc.Note, tc.Clause, tc.Expect),
			Run: func(t *hivesim.T) {
				info := c.verify(fixtures, tc.Snapshot, tc.Preimages)
				if tc.Expect == expectUnspecified {
					// Reported, never scored: the EIP does not settle these.
					t.Logf("unspecified clause %s: the client %s it (exit %d)",
						tc.Clause, accepted(info.ExitCode), info.ExitCode)
					return
				}
				scored++
				switch info.ExitCode {
				case exitReject:
					if strings.TrimSpace(info.Stderr) == "" {
						t.Fatalf("rejected the artifacts but said nothing about why")
					}
					passed++
				case exitAccept:
					t.Fatalf("accepted an artifact that breaks %s: %s", tc.Clause, tc.Note)
				case exitUnsupported:
					t.Fatalf("unsupported: the client stopped supporting verify mid-suite")
				default:
					t.Fatalf("crash: verify exited %d\n%s", info.ExitCode, info.Stderr)
				}
			},
		})
	}
	report.set(key, fmt.Sprintf("%d/%d", passed, scored))
}

// runConvert measures the production leg. A client may produce one artifact
// and not the other (erigon writes the preimage file and has no portable
// snapshot), so each is judged on its own and whatever comes back is
// registered with the producer set for the cross-client agreement check.
func runConvert(t *hivesim.T, c *client, fixtures *manifest, report *report, producers *producerSet, anchored bool) {
	if !anchored {
		report.set("produce_preimages", "inconclusive")
		report.set("produce_snapshot", "inconclusive")
		return
	}
	var (
		produced = map[string][]byte{}
		root     string
		stderr   string
	)
	t.Run(hivesim.TestSpec{
		Name:        fmt.Sprintf("%s/convert/run", c.Type),
		Description: "Converting the anchor state must succeed and hand back at least one artifact.",
		Run: func(t *hivesim.T) {
			info := c.run("convert", anchor)
			stderr = info.Stderr
			switch info.ExitCode {
			case exitUnsupported:
				report.set("produce_preimages", "unsupported")
				report.set("produce_snapshot", "unsupported")
				t.Fatalf("unsupported: this client converts no artifact\n%s", info.Stderr)
			case exitAccept:
			default:
				report.set("produce_preimages", "crash")
				report.set("produce_snapshot", "crash")
				t.Fatalf("crash: convert exited %d\n%s", info.ExitCode, info.Stderr)
			}
			// Each artifact is optional: a client that emits neither has
			// nothing to say and should have reported unsupported instead.
			for name, re := range map[string]*regexp.Regexp{"snapshot": snapshotRE, "preimages": preimagesRE} {
				if raw := match(re, info.Stdout); raw != "" {
					blob, err := base64.StdEncoding.DecodeString(raw)
					if err != nil {
						t.Fatalf("the %s the client produced is not valid base64: %v", name, err)
					}
					produced[name] = blob
				}
			}
			if len(produced) == 0 {
				t.Fatalf("convert exited 0 but printed neither snapshot= nor preimages=\n%s", info.Stdout)
			}
			root = match(rootRE, info.Stdout)
		},
	})
	if len(produced) == 0 {
		return
	}

	// A PBT root is only meaningful from a client that built the tree, so it
	// is required of a snapshot producer and not asked of anyone else.
	if _, ok := produced["snapshot"]; ok {
		t.Run(hivesim.TestSpec{
			Name:        fmt.Sprintf("%s/convert/root", c.Type),
			Description: "A snapshot producer must reproduce the PBT root the fixtures carry.",
			Run: func(t *hivesim.T) {
				if root == "" {
					t.Fatalf("produced a snapshot but printed no root=")
				}
				if !strings.EqualFold(root, fixtures.Genesis.PBTRoot) {
					t.Fatalf("converted to PBT root %s, the fixtures carry %s", root, fixtures.Genesis.PBTRoot)
				}
			},
		})
	}

	for _, name := range []string{"preimages", "snapshot"} {
		blob, ok := produced[name]
		if !ok {
			report.set("produce_"+name, "unsupported")
			t.Run(hivesim.TestSpec{
				Name:        fmt.Sprintf("%s/convert/%s-NOT-PRODUCED", c.Type, name),
				Description: "This client produces the other artifact but not this one.",
				Run:         func(t *hivesim.T) { t.Fatalf("unsupported: no %s produced\n%s", name, stderr) },
			})
			continue
		}
		producers.add(name, c.Type, blob)

		var equal bool
		t.Run(hivesim.TestSpec{
			Name: fmt.Sprintf("%s/convert/bytes-%s", c.Type, name),
			Description: `Both artifacts are byte-canonical, so an independent producer of the
same anchor state must emit the same bytes.`,
			Run: func(t *hivesim.T) {
				want, err := fixtures.read(canonical(fixtures, name))
				if err != nil {
					t.Fatalf("crash: %v", err)
				}
				if diff := firstDiff(blob, want); diff >= 0 {
					t.Fatalf("the %s file differs from the canonical bytes at byte %d (%d produced, %d expected)",
						name, diff, len(blob), len(want))
				}
				equal = true
			},
		})
		if equal {
			report.set("produce_"+name, "byte identical")
		} else {
			report.set("produce_"+name, "DIFFERS")
		}
	}
}

// canonical names the fixture file an artifact is compared against.
func canonical(fixtures *manifest, artifact string) string {
	if artifact == "snapshot" {
		return fixtures.Valid.Snapshot
	}
	return fixtures.Valid.Preimages
}

// producerSet collects what every client produced, so the run can ask the
// question no single client can answer: do the independent producers of a
// byte-canonical artifact actually agree?
type producerSet struct {
	mu sync.Mutex
	by map[string][]producerResult
}

type producerResult struct {
	client string
	blob   []byte
}

func newProducerSet() *producerSet {
	return &producerSet{by: make(map[string][]producerResult)}
}

func (p *producerSet) add(artifact, client string, blob []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.by[artifact] = append(p.by[artifact], producerResult{client: client, blob: blob})
}

// runAgreement is the cross-client verdict, emitted once per artifact after
// every client has had its turn.
//
// Byte-canonicality is a claim about producers as a set, not about any one of
// them: independent producers of the same anchor state must emit identical
// bytes. So a producer that diverges fails, and a run where *no* producer
// reproduces the canonical bytes fails hardest, because then either every
// implementation is wrong or the canonical bytes are, and both are worse than
// one client being broken.
func runAgreement(t *hivesim.T, fixtures *manifest, producers *producerSet) {
	producers.mu.Lock()
	defer producers.mu.Unlock()

	for _, artifact := range []string{"preimages", "snapshot"} {
		results := producers.by[artifact]
		t.Run(hivesim.TestSpec{
			Name: fmt.Sprintf("agreement/%s", artifact),
			Description: `Every client that produces this artifact must emit the same bytes as
every other and as the canonical fixture.`,
			Run: func(t *hivesim.T) {
				want, err := fixtures.read(canonical(fixtures, artifact))
				if err != nil {
					t.Fatalf("crash: %v", err)
				}
				var agree, differ []string
				for _, r := range results {
					if firstDiff(r.blob, want) < 0 {
						agree = append(agree, r.client)
					} else {
						differ = append(differ, r.client)
					}
				}
				switch {
				case len(results) == 0:
					t.Fatalf("inconclusive: no client produced a %s, so agreement is unmeasured", artifact)
				case len(agree) == 0:
					t.Fatalf("NO PRODUCER reproduces the canonical %s: %s all differ. Either every implementation is wrong or the fixture is",
						artifact, strings.Join(differ, ", "))
				case len(differ) > 0:
					t.Fatalf("%s disagree on the %s bytes while %s match",
						strings.Join(differ, ", "), artifact, strings.Join(agree, ", "))
				case len(agree) == 1:
					t.Fatalf("inconclusive: only %s produced a %s, so agreement between independent producers is unproven",
						agree[0], artifact)
				default:
					t.Logf("%d independent producers agree byte for byte: %s", len(agree), strings.Join(agree, ", "))
				}
			},
		})
	}
}

// skipAll records one row per case saying why it was not run, so a client
// that cannot take part is visible without inventing failures for it.
func skipAll(t *hivesim.T, clientName, suite string, cases []testCase, reason string) {
	t.Run(hivesim.TestSpec{
		Name:        fmt.Sprintf("%s/%s/NOT-RUN", clientName, suite),
		Description: fmt.Sprintf("%d fixtures were not run.", len(cases)),
		Run:         func(t *hivesim.T) { t.Fatal(reason) },
	})
}

// report is the per-client row of the capability matrix.
type report struct {
	name, clientType string
	mu               sync.Mutex
	fields           map[string]string
	order            []string
}

func newReport(name, clientType string) *report {
	return &report{name: name, clientType: clientType, fields: make(map[string]string)}
}

func (r *report) set(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.fields[key]; !seen {
		r.order = append(r.order, key)
	}
	r.fields[key] = value
}

// publish writes the row as its own always-passing test, so the matrix
// survives in the results JSON whatever the individual cases did.
func (r *report) publish(t *hivesim.T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "client=%s", r.name)
	for _, k := range r.order {
		fmt.Fprintf(&b, " %s=%s", k, strings.ReplaceAll(r.fields[k], " ", "-"))
	}
	summary := b.String()
	t.Run(hivesim.TestSpec{
		Name:        fmt.Sprintf("%s/capability-matrix", r.clientType),
		Description: "One line of the capability matrix; always passes, and carries the row in its details.",
		Run:         func(t *hivesim.T) { t.Log(summary) },
	})
}

// shimFor returns the script that drives a client. Hive names a client
// <client>_<nametag> once a nametag or build argument is in play, so the
// lookup falls back to the part before the underscore. A client with no shim
// at all gets the generic one, which reports every verb unsupported: an
// unknown client is a gap in the matrix, not a failure.
func shimFor(clientName string) string {
	for _, name := range []string{clientName, strings.SplitN(clientName, "_", 2)[0]} {
		path := filepath.Join("shims", name+".sh")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join("shims", "unsupported.sh")
}

func match(re *regexp.Regexp, out string) string {
	if m := re.FindStringSubmatch(out); len(m) == 2 {
		return m[1]
	}
	return ""
}

// firstDiff returns the offset of the first differing byte, or -1.
func firstDiff(got, want []byte) int {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return i
		}
	}
	if len(got) != len(want) {
		return min(len(got), len(want))
	}
	return -1
}

func accepted(code int) string {
	if code == exitAccept {
		return "accepts"
	}
	return "rejects"
}
