package satellite

import (
	"testing"
	"time"
)

func TestAssignIDsLineage(t *testing.T) {
	base := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)

	t.Run("a: new component gets FireID of its anchor", func(t *testing.T) {
		d1 := gridDetection("N20", "2026-11-12", "200", -2.5000, 113.8000, "n", base)
		d2 := gridDetection("N20", "2026-11-12", "205", -2.5080, 113.8000, "n", base.Add(5*time.Minute))
		components := [][]Detection{{d1, d2}}
		ids, retracted, created := Assign(components, components, map[string]string{}, map[string]FireMeta{})
		want := FireID(d1.Key()) // d1 is earliest -> anchor
		if len(ids) != 1 || ids[0] != want {
			t.Fatalf("ids = %v, want [%s]", ids, want)
		}
		if len(retracted) != 0 {
			t.Fatalf("retracted = %v, want none", retracted)
		}
		if anchor, ok := created[want]; !ok || anchor.Key != d1.Key() {
			t.Fatalf("created[%s] = %v, ok=%v, want anchor %s", want, anchor, ok, d1.Key())
		}
	})

	t.Run("b: merge keeps the earlier anchor and retracts the later one", func(t *testing.T) {
		anchorA := gridDetection("N20", "2026-11-12", "100", -2.5000, 113.8000, "n", base)
		anchorB := gridDetection("N20", "2026-11-12", "200", -3.0000, 114.0000, "n", base.Add(time.Hour))
		bridge := gridDetection("N20", "2026-11-12", "150", -2.7500, 113.9000, "n", base.Add(30*time.Minute))

		id1, id2 := FireID(anchorA.Key()), FireID(anchorB.Key())
		prev := map[string]string{anchorA.Key(): id1, anchorB.Key(): id2}
		fires := map[string]FireMeta{
			id1: {Anchor: Anchor{Key: anchorA.Key(), At: anchorA.At}, Active: true},
			id2: {Anchor: Anchor{Key: anchorB.Key(), At: anchorB.At}, Active: true},
		}

		components := [][]Detection{{anchorA, bridge, anchorB}}
		ids, retracted, _ := Assign(components, components, prev, fires)
		if len(ids) != 1 || ids[0] != id1 {
			t.Fatalf("ids = %v, want [%s] (earlier anchor survives)", ids, id1)
		}
		if len(retracted) != 1 || retracted[0] != id2 {
			t.Fatalf("retracted = %v, want [%s]", retracted, id2)
		}
	})

	t.Run("c: split with the anchor still active keeps the anchor's side", func(t *testing.T) {
		anchorA := gridDetection("N20", "2026-11-12", "100", -2.5000, 113.8000, "n", base)
		bridge := gridDetection("N20", "2026-11-12", "150", -2.7500, 113.9000, "n", base.Add(10*time.Minute))
		memberC := gridDetection("N20", "2026-11-12", "200", -3.0000, 114.0000, "n", base.Add(20*time.Minute))
		id1 := FireID(anchorA.Key())
		prev := map[string]string{anchorA.Key(): id1, bridge.Key(): id1, memberC.Key(): id1}
		fires := map[string]FireMeta{id1: {Anchor: Anchor{Key: anchorA.Key(), At: anchorA.At}, Active: true}}

		// The bridge is gone this batch; anchorA now pairs with a new
		// nearby detection, and memberC pairs with its own new neighbor.
		newNearA := gridDetection("N20", "2026-11-12", "101", -2.5003, 113.8000, "n", base.Add(time.Minute))
		newNearC := gridDetection("N20", "2026-11-12", "201", -3.0003, 114.0000, "n", base.Add(21*time.Minute))

		components := [][]Detection{{anchorA, newNearA}, {memberC, newNearC}}
		ids, retracted, created := Assign(components, components, prev, fires)
		if ids[0] != id1 {
			t.Fatalf("ids[0] = %s, want %s (contains the still-active anchor detection)", ids[0], id1)
		}
		if ids[1] == id1 {
			t.Fatalf("ids[1] must be a different, brand-new ID, got the old one")
		}
		if _, ok := created[ids[1]]; !ok {
			t.Fatalf("created must record the brand-new split-off fire %s", ids[1])
		}
		for _, r := range retracted {
			if r == id1 {
				t.Fatalf("id1 must not be retracted: it survives on the anchor's side")
			}
		}
	})

	t.Run("d: split after the anchor itself is gone keeps the earliest remaining member's side", func(t *testing.T) {
		anchorA := gridDetection("N20", "2026-11-12", "100", -2.5000, 113.8000, "n", base)
		memberB := gridDetection("N20", "2026-11-12", "110", -2.5080, 113.8000, "n", base.Add(10*time.Minute))
		memberC := gridDetection("N20", "2026-11-12", "300", -3.0000, 114.0000, "n", base.Add(2*time.Hour))
		id1 := FireID(anchorA.Key())
		prev := map[string]string{anchorA.Key(): id1, memberB.Key(): id1, memberC.Key(): id1}
		fires := map[string]FireMeta{id1: {Anchor: Anchor{Key: anchorA.Key(), At: anchorA.At}, Active: true}}

		// anchorA itself is retracted/absent this batch; B and C split
		// into two separate components (their own new partners).
		newNearB := gridDetection("N20", "2026-11-12", "111", -2.5083, 113.8000, "n", base.Add(11*time.Minute))
		newNearC := gridDetection("N20", "2026-11-12", "301", -3.0003, 114.0000, "n", base.Add(2*time.Hour+time.Minute))

		components := [][]Detection{{memberB, newNearB}, {memberC, newNearC}}
		ids, _, _ := Assign(components, components, prev, fires)
		if ids[0] != id1 {
			t.Fatalf("ids[0] = %s, want %s (B is the earliest surviving former member)", ids[0], id1)
		}
		if ids[1] == id1 {
			t.Fatalf("ids[1] must be a new ID; C is not the earliest surviving former member")
		}
	})

	t.Run("e: a later-arriving but earlier-timed detection re-anchors the fire (PR #6 review fix)", func(t *testing.T) {
		existing := gridDetection("N20", "2026-11-12", "200", -2.5000, 113.8000, "n", base)
		id1 := "fire-deadbeefdeadbeef" // arbitrary pre-existing ID, unrelated to any detection's own hash
		prev := map[string]string{existing.Key(): id1}
		fires := map[string]FireMeta{id1: {Anchor: Anchor{Key: existing.Key(), At: existing.At}, Active: true}}

		// A genuinely earlier detection (late arrival, e.g. the far
		// side of a disconnect/reconnect) joins the same component
		// this batch. Keeping id1 here would make the fire's final
		// ID depend on which batch boundary happened to exist at
		// creation time rather than on the final detection set alone
		// (P4.5/P4.8, PR #6 review finding) -- so id1 is retracted
		// and a fresh ID is minted from the true (earlier) anchor.
		earlier := gridDetection("N20", "2026-11-12", "150", -2.5040, 113.8000, "n", base.Add(-50*time.Minute))

		components := [][]Detection{{existing, earlier}}
		ids, retracted, created := Assign(components, components, prev, fires)
		wantID := FireID(earlier.Key())
		if ids[0] != wantID {
			t.Fatalf("ids[0] = %s, want %s (re-anchored on the true earliest member)", ids[0], wantID)
		}
		if len(retracted) != 1 || retracted[0] != id1 {
			t.Fatalf("retracted = %v, want exactly [%s]", retracted, id1)
		}
		if anchor, ok := created[wantID]; !ok || anchor.Key != earlier.Key() {
			t.Fatalf("created[%s] = %+v, want anchor key %s", wantID, anchor, earlier.Key())
		}
	})

	t.Run("f: colliding with an active fire's anchor key gets a deterministic suffix", func(t *testing.T) {
		x := gridDetection("N20", "2026-11-12", "500", 1.0000, 100.0000, "n", base)
		y := gridDetection("N20", "2026-11-12", "505", 0.9920, 100.0000, "n", base.Add(5*time.Minute))
		collidingID := FireID(x.Key())
		fires := map[string]FireMeta{collidingID: {Anchor: Anchor{Key: x.Key(), At: x.At}, Active: true}}

		components := [][]Detection{{x, y}}
		ids, _, created := Assign(components, components, map[string]string{}, fires)
		want := FireID(x.Key() + "#1")
		if ids[0] != want {
			t.Fatalf("ids[0] = %s, want %s (deterministic suffix, not colliding with the active fire)", ids[0], want)
		}
		if _, ok := created[want]; !ok {
			t.Fatalf("created must record the suffixed fire")
		}
	})
}

func TestFireIDIsDeterministic(t *testing.T) {
	a := FireID("N20|2026-11-12|243|-2.51230|113.80440")
	b := FireID("N20|2026-11-12|243|-2.51230|113.80440")
	if a != b {
		t.Fatalf("FireID is not deterministic: %s vs %s", a, b)
	}
	if FireID("other") == a {
		t.Fatalf("different anchors must not collide")
	}
}
