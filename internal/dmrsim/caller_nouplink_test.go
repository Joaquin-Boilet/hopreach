package dmrsim

import (
	"testing"

	"hopreach/internal/dmr"
)

// f64 keeps *float64 radio coordinates readable in table-y tests.
func f64(x float64) *float64 { return &x }

// TestCallerNoUplinkExplained covers the caller-side dead-key cases: the
// topology's most useful failure is the one the operator *performs* —
// pressing PTT from a radio that cannot key its repeater. It must come
// back explained (status no_uplink, reason on the caller's own row),
// neither charged to the air ledger nor claiming the talkgroup, and the
// scenario must keep running (no hard error kills the whole report).
func TestCallerNoUplinkExplained(t *testing.T) {
	base := oneRepNet()

	// ── Case 1: caller without coordinates → reason no_position ────────
	unplaced := dmr.Radio{
		Name: "HT-SIN-POS", Role: "handy", Model: "dp4801e",
		Talkgroups: []string{"FAENA1"},
	}
	net1 := withNet(base, []dmr.Radio{unplaced}, nil)
	sc1 := Scenario{Calls: []Call{{From: "HT-SIN-POS", TG: "FAENA1", AtMS: 0, DurationMS: 2000}}}
	rep1, err := Run(net1, links1(nil, nil), sc1, true)
	if err != nil {
		t.Fatalf("caller sin posición debe ser resultado, no error: %v", err)
	}
	if got := rep1.Calls[0].Status; got != "no_uplink" {
		t.Fatalf("status = %q, quiero no_uplink", got)
	}
	d := rep1.Calls[0].Deliveries[0]
	if d.Delivered || d.Reason != "no_position" {
		t.Fatalf("caller row = %+v, quiero no_position sin entrega", d)
	}
	if len(rep1.Loads) != 0 {
		t.Fatalf("ledger cobró %v: una llamada que no al aire no cuesta aire", rep1.Loads)
	}

	// ── Case 2: caller positioned, its only uplink is negative →
	// reason no_margin; the report still names the site/slot it WOULD
	// have keyed (that's what the operator needs to know). ─────────────
	valle := dmr.Radio{
		Name: "HT-VALLE", Role: "handy", Model: "dp4801e",
		Lat: f64(-33.40), Lon: f64(-70.44),
		Talkgroups: []string{"FAENA1"},
	}
	net2 := withNet(base, []dmr.Radio{valle}, nil)
	lk := dmr.NetworkLinks{
		ServingOf: map[string]string{"HT-VALLE": "REP-A"},
		Uplinks: []dmr.Link{{
			Repeater: "REP-A", Radio: "HT-VALLE", Dir: dmr.Uplink,
			MarginDB: -3.4, DistanceKm: 12,
		}},
		Downlinks: []dmr.Link{{
			Repeater: "REP-A", Radio: "HT-VALLE", Dir: dmr.Downlink,
			MarginDB: -3.4, DistanceKm: 12,
		}},
	}
	sc2 := Scenario{Calls: []Call{{From: "HT-VALLE", TG: "FAENA1", AtMS: 100, DurationMS: 3000}}}
	rep2, err := Run(net2, lk, sc2, true)
	if err != nil {
		t.Fatalf("no_margin caller: %v", err)
	}
	c2 := rep2.Calls[0]
	if c2.Status != "no_uplink" {
		t.Fatalf("status = %q, quiero no_uplink", c2.Status)
	}
	if c2.Site != "REP-A" || c2.Slot != 1 {
		t.Fatalf("el informe debe seguir diciendo DÓNDE habría keyeado: site=%q slot=%d", c2.Site, c2.Slot)
	}
	if c2.Deliveries[0].Reason != "no_margin" {
		t.Fatalf("razón = %q, quiero no_margin", c2.Deliveries[0].Reason)
	}
	if len(rep2.Loads) != 0 {
		t.Fatalf("ledger cobró %v: una llamada sin uplink no cuesta aire", rep2.Loads)
	}

	// ── Case 3: a following call in the same scenario still reports. ───
	sc3 := Scenario{Calls: []Call{
		{From: "HT-VALLE", TG: "FAENA1", AtMS: 100, DurationMS: 3000},
		{From: "HT-VALLE", TG: "FAENA1", AtMS: 4000, DurationMS: 1000},
	}}
	rep3, err := Run(net2, lk, sc3, true)
	if err != nil {
		t.Fatalf("el escenario debe sobrevivir a un no_uplink: %v", err)
	}
	if len(rep3.Calls) != 2 {
		t.Fatalf("calls=%d, quiero 2", len(rep3.Calls))
	}
	for i, c := range rep3.Calls {
		if c.Status != "no_uplink" {
			t.Fatalf("llamada %d status = %q", i+1, c.Status)
		}
	}
	if len(rep3.Loads) != 0 {
		t.Fatalf("ledger cobró %v", rep3.Loads)
	}
}
