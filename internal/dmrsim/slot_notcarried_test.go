package dmrsim

import (
	"testing"

	"hopreach/internal/dmr"
)

// TestSlotNotCarried: a talkgroup pinned to a slot the keying repeater
// does not carry is an operator-configuration outcome — the PTT dies at
// the site (nothing airs, no ledger, no TG claim), reported as
// slot_not_carried on the caller's own row.
func TestSlotNotCarried(t *testing.T) {
	n := oneRepNet() // REP-A carries TS1 only
	// EMERGENCIA is a network-level TG pinned to TS2 — the site just
	// doesn't carry TS2, which is exactly the case under test.
	n.Talkgroups = append(n.Talkgroups, dmr.Talkgroup{Name: "EMERGENCIA", ID: "9", Slot: 2})
	rd := dmr.Radio{
		Name: "R1", Role: "handy", Model: "dp4801e",
		Talkgroups: []string{"FAENA1", "EMERGENCIA"},
	}
	n = withNet(n, []dmr.Radio{rd}, nil)
	lk := links1(map[string]string{"R1": "REP-A"}, map[string]float64{"R1": 6})

	sc := Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 0, DurationMS: 1000},       // TS1 → airs
		{From: "R1", TG: "EMERGENCIA", AtMS: 1200, DurationMS: 1000}, // TS2 → not carried
	}}
	rep, err := Run(n, lk, sc, true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Calls[0].Status != "aired" {
		t.Fatalf("llamada 1 status=%q", rep.Calls[0].Status)
	}
	c2 := rep.Calls[1]
	if c2.Status != "not_carried" {
		t.Fatalf("llamada 2 status=%q, quiero not_carried", c2.Status)
	}
	if c2.Deliveries[0].Reason != "slot_not_carried" {
		t.Fatalf("razón=%q", c2.Deliveries[0].Reason)
	}
	// Loads: only the TS1 call charged air.
	if len(rep.Loads) != 1 || rep.Loads[0].Slot != 1 || rep.Loads[0].TransmitMS != 1000 {
		t.Fatalf("loads=%+v, quiero solo REP-A TS1 1000ms", rep.Loads)
	}
	// The TG claim: EMERGENCIA was never aired, so nothing claimed it —
	// covered by the loads assertion above (no second charge anywhere).
}
