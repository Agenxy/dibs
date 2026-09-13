package supgang

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a stand-in `supgang` that prints the JSON the
// real one printed on 2026-09-13 (supgang 0.2.0-alpha.10), so the parsing is
// exercised against real envelopes and not against what this package hoped
// they looked like.
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_SUPGANG_SLEEP") != "" {
		<-time.After(20 * time.Second) // the lingering descendant, holding the inherited pipe
		os.Exit(0)
	}
	if os.Getenv("DIBS_TEST_AS_SUPGANG") != "" {
		os.Exit(fakeSupgang(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeSupgang(args []string) int {
	if len(args) < 2 || args[0] != "--json" {
		fmt.Fprintln(os.Stderr, "fake supgang: expected --json first")
		return 2
	}
	if os.Getenv("DIBS_TEST_SUPGANG_LINGER") != "" {
		// Print a valid answer, then start a descendant that inherits stdout
		// and outlives us: the shape that kept Run waiting on the pipe.
		_, _ = os.Stdout.WriteString(`{"schema":"supgang.status/v4","status":"ok","name":"x","node_id":"` + strings.Repeat("a", 64) + `"}`)
		child := exec.Command(os.Args[0], "-test.run=XXX")
		child.Env = append(os.Environ(), "DIBS_TEST_AS_SUPGANG=", "DIBS_TEST_SUPGANG_SLEEP=1")
		child.Stdout = os.Stdout
		_ = child.Start()
		return 0
	}
	if os.Getenv("DIBS_TEST_SUPGANG_UNINITIALISED") != "" {
		_, _ = os.Stdout.WriteString(`{"schema":"supgang.error/v1","status":"error","error":"local control state directory failed validation"}`)
		return 3 // as the real one: a refusal is an envelope AND a non-zero exit
	}
	// DIBS_TEST_SUPGANG_SERVICES makes the fake a Supgang that carries
	// service advertisements (peers/v6, resolve/v5): MacSolis says it runs
	// dibs on 4790 behind a key, plus one row that is not an advertisement.
	services, peersSchema, resolveSchema := "", "supgang.peers/v5", "supgang.resolve/v4"
	if os.Getenv("DIBS_TEST_SUPGANG_SERVICES") != "" {
		services = `,"services":[{"name":"dibs","port":4790,"key_pin":"` + strings.Repeat("ab", 32) + `"},{"name":"","port":1,"key_pin":"zz"}]`
		peersSchema, resolveSchema = "supgang.peers/v6", "supgang.resolve/v5"
	}
	switch args[1] {
	case "status":
		_, _ = os.Stdout.WriteString(`{"schema":"supgang.status/v4","status":"ok","version":"0.2.0-alpha.10","name":"MacMarine","hive_id":"5cb4e356","node_id":"fed08b444ee029ef43b8c04106199408f449d6857f60c16423d32f8dbbe77621","service":"running","listen":"[::]:44330","active_peers":1,"known_peers":1,"router_mapping":"disabled","internet_reachability":"direct-address-unverified","connection_recovery":"automatic-multi-path","mode":"device"}`)
	case "peers":
		_, _ = os.Stdout.WriteString(`{"schema":"` + peersSchema + `","status":"ok","this_computer":{"name":"MacMarine","name_source":"device-signed","tags":[],"fingerprint":"fed08b44","node_id":"fed08b444ee029ef43b8c04106199408f449d6857f60c16423d32f8dbbe77621","connected":true,"status":"running","generation":0,"sequence":352,"expires_at":1789322147,"candidate_count":2,"addresses":[{"scope":"local","kind":"local","transport":"quic-v1","address":"192.168.1.205:44330","provenance":"device-signed","route_compatible":true,"preferred":true},{"scope":"public","kind":"direct","transport":"quic-v1","address":"[2600:1700:2f70:ce40::41]:44330","provenance":"device-signed","route_compatible":true,"preferred":false}]},"peers":[{"name":"MacSolis","name_source":"device-signed","tags":["solis"],"fingerprint":"a8a37e32","node_id":"a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152","connected":true,"status":"fresh","generation":0,"sequence":217,"expires_at":1789321427,"candidate_count":2,"addresses":[{"scope":"local","kind":"local","transport":"quic-v1","address":"192.168.1.191:44330","provenance":"device-signed","route_compatible":true,"preferred":true},{"scope":"public","kind":"direct","transport":"quic-v1","address":"[2600:1700:2f70:ce40::c]:44330","provenance":"device-signed","route_compatible":true,"preferred":false}]` + services + `}]}`)
	case "resolve":
		// "--state-dir" is ANSWERED here on purpose: the production guard is
		// what must stop a flag-shaped peer reaching argv, and a fake that
		// refused it would pass a build with the guard removed.
		if len(args) < 3 || args[2] != "MacSolis" && args[2] != "solis" && args[2] != "a8a37e32" && args[2] != "--state-dir" {
			_, _ = os.Stdout.WriteString(`{"schema":"supgang.error/v1","status":"error","error":"no known peer significantly matches that name, tag, or fingerprint"}`)
			return 3
		}
		_, _ = os.Stdout.WriteString(`{"schema":"` + resolveSchema + `","status":"ok","node_id":"a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152","name":"MacSolis","tags":["solis"],"fingerprint":"a8a37e32","generation":0,"sequence":217,"issued_at":1789299827,"expires_at":1789321427,"candidates":[{"scope":"local","kind":"local","transport":"quic-v1","address":"192.168.1.191:44330","provenance":"device-signed","route_compatible":true,"preferred":true},{"scope":"public","kind":"direct","transport":"quic-v1","address":"[2600:1700:2f70:ce40::c]:44330","provenance":"device-signed","route_compatible":true,"preferred":false}]` + services + `}`)
	default:
		fmt.Fprintln(os.Stderr, "fake supgang: unknown command", args[1])
		return 2
	}
	return 0
}

func useFake(t *testing.T) {
	t.Helper()
	old := Command
	Command = os.Args[0]
	t.Setenv("DIBS_TEST_AS_SUPGANG", "1")
	t.Cleanup(func() { Command = old })
}

func TestTheIdentityAndPeersAreReadFromSupgangsOwnEnvelopes(t *testing.T) {
	useFake(t)
	ctx := context.Background()
	id, err := Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id.NodeID != "fed08b444ee029ef43b8c04106199408f449d6857f60c16423d32f8dbbe77621" || id.Name != "MacMarine" || id.Fingerprint != "" {
		t.Errorf("Status = %+v", id)
	}
	self, peers, err := Peers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if self.Fingerprint != "fed08b44" || len(peers) != 1 || peers[0].Name != "MacSolis" || !peers[0].Connected {
		t.Errorf("Peers = %+v %+v", self, peers)
	}
	p, err := Resolve(ctx, "solis")
	if err != nil {
		t.Fatal(err)
	}
	if p.NodeID != peers[0].NodeID || p.Address() != "192.168.1.191:44330" {
		t.Errorf("Resolve = %+v, Address = %q: want the preferred route-compatible candidate", p, p.Address())
	}
	if p.ServicesKnown || peers[0].ServicesKnown || len(p.Services) != 0 {
		t.Errorf("a Supgang without advertisements reported some: %+v", p)
	}
}

// A Supgang that carries service advertisements (ADR 0002) answers with the
// next schema majors, which add `services` and nothing else: both are read,
// the advertisement is found by name, a row that is not an advertisement is
// dropped, and the answer says advertisements were carried at all, which is
// the difference between "runs nothing" and "an older Supgang".
func TestServiceAdvertisementsAreReadWhenSupgangCarriesThem(t *testing.T) {
	useFake(t)
	t.Setenv("DIBS_TEST_SUPGANG_SERVICES", "1")
	ctx := context.Background()
	p, err := Resolve(ctx, "solis")
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := p.Service(ServiceName)
	if !p.ServicesKnown || !ok || svc.Port != 4790 || svc.KeyPin != strings.Repeat("ab", 32) || svc.Valid() != nil {
		t.Errorf("Resolve = %+v: want a valid dibs advertisement", p)
	}
	// The malformed row is KEPT and judged where it is used: dropped, a
	// broken advertisement for the service a caller wants would read as no
	// advertisement, and the join would fall back to an unpinned ceremony.
	if len(p.Services) != 2 || p.Services[1].Valid() == nil {
		t.Errorf("Resolve = %+v: want the malformed row kept and invalid", p)
	}
	if _, ok := p.Service("remap"); ok {
		t.Error("an advertisement nobody made was found")
	}
	_, peers, err := Peers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || !peers[0].ServicesKnown || len(peers[0].Services) != 2 {
		t.Errorf("Peers = %+v: want the same advertisements", peers)
	}
	for _, bad := range []Service{{Name: "", Port: 1, KeyPin: svc.KeyPin}, {Name: "dibs", Port: 0, KeyPin: svc.KeyPin}, {Name: "dibs", Port: 70000, KeyPin: svc.KeyPin}, {Name: "dibs", Port: 1, KeyPin: "ab"}} {
		if bad.Valid() == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	for _, pin := range []string{"", "ab", strings.Repeat("AB", 32), strings.Repeat("zz", 32)} {
		if checkKeyPin(pin) == nil {
			t.Errorf("key pin %q was accepted", pin)
		}
	}
}

// Supgang's own words for a computer that has not joined a hive, and for a
// name it does not know: a Dibs operator reading doctor should see them.
func TestSupgangsErrorsArrivedVerbatim(t *testing.T) {
	useFake(t)
	ctx := context.Background()
	_, err := Resolve(ctx, "nobody")
	var se *Error
	if !errors.As(err, &se) || !strings.Contains(se.Message, "no known peer") {
		t.Errorf("Resolve(nobody) = %v, want Supgang's own sentence", err)
	}
	// A peer that could read as a flag never reaches argv: the fake would
	// answer it, so only the guard can produce this error.
	if _, err := Resolve(ctx, "--state-dir"); err == nil || !strings.Contains(err.Error(), "not a peer name") {
		t.Errorf("a flag-shaped peer reached supgang: %v", err)
	}
	t.Setenv("DIBS_TEST_SUPGANG_UNINITIALISED", "1")
	if _, err := Status(ctx); !errors.As(err, &se) || !strings.Contains(err.Error(), "state directory") {
		t.Errorf("Status on an uninitialised machine = %v", err)
	}
}

// A schema-valid answer with no usable node id is refused, not stamped on a
// machine's agents: the daemon once sliced an empty one at boot.
func TestAnAnswerWithoutANodeIDIsRefused(t *testing.T) {
	for _, id := range []string{"", "fed08b44", strings.Repeat("Z", 64), strings.Repeat("a", 63)} {
		if err := checkNodeID(id); err == nil {
			t.Errorf("node id %q was accepted", id)
		}
	}
	if err := checkNodeID(strings.Repeat("0f", 32)); err != nil {
		t.Errorf("a well-formed node id was refused: %v", err)
	}
	var out struct{ envelope }
	out.Schema, out.Status = "supgang.status/v4", "warning"
	if _, err := out.check("supgang.status/", 4); err == nil {
		t.Error("a status that is neither ok nor error was accepted")
	}
}

// The deadline bounds the pipes as well as the process: a stand-in that
// hands its stdout to a descendant and exits does not hold Status open for
// the descendant's lifetime.
func TestADescendantHoldingThePipeDoesNotHoldTheAnswer(t *testing.T) {
	useFake(t)
	t.Setenv("DIBS_TEST_SUPGANG_LINGER", "1")
	start := time.Now()
	_, err := Status(context.Background())
	if took := time.Since(start); took > callTimeout+4*time.Second {
		t.Fatalf("Status took %s with a descendant holding the pipe; the deadline did not bound it", took)
	}
	if err == nil {
		t.Error("an answer whose process outlived the deadline was accepted")
	}
}

// A schema this build does not read is a named error, not a wrong host id.
func TestAnUnknownSchemaIsRefusedByName(t *testing.T) {
	var out struct{ envelope }
	out.Schema = "supgang.status/v9"
	out.Status = "ok"
	if _, err := out.check("supgang.status/", 4); err == nil || !strings.Contains(err.Error(), "v9") || !strings.Contains(err.Error(), "v4") {
		t.Errorf("check = %v, want both schemas named", err)
	}
	// Two accepted majors: the one that answered is returned, and neither
	// an older nor a newer one passes.
	out.Schema = "supgang.peers/v6"
	if major, err := out.check("supgang.peers/", 5, 6); err != nil || major != 6 {
		t.Errorf("check(v6 of 5,6) = %d %v", major, err)
	}
	for _, s := range []string{"supgang.peers/v4", "supgang.peers/v7"} {
		out.Schema = s
		if _, err := out.check("supgang.peers/", 5, 6); err == nil {
			t.Errorf("%s was accepted", s)
		}
	}
}

func TestNotInstalledIsItsOwnAnswer(t *testing.T) {
	old := Command
	Command = "/nonexistent/supgang-" + t.Name()
	t.Cleanup(func() { Command = old })
	if Available() {
		t.Fatal("a missing binary reported available")
	}
	if _, err := Status(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Status without the binary = %v", err)
	}
}
