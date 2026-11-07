package dmrsim

// Tests exercise the engine in isolation: they hand it resolved links
// directly, so a physics change can never扰动 break protocol tests. The
// one physics test (TestLinksFlatGroundMatchesFSPL) checks that
// dmr.ComputeLinks over flat ground reproduces plain free-space arithmetic
// — the seal between the DMR link layer and internal/propagation.

import (
	"math"
	"testing"

	"hopreach/internal/dmr"
)

func floatEq(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// oneRepNet is the canonical one-repeater test network (REP-A at an
// unspecified position; tests hand links explicitly so the engine stays
// free of terrain).
func oneRepNet() dmr.Network {
	r := dmr.Repeater{
		Name: "REP-A", Model: "slr5500",
		TXMHz: 454.175, RXMHz: 449.175,
		TxPowerW: 50, AntennaGainDBi: 3, RxSensitivityDBm: -112, AntennaHeightM: 15,
	}
	r.Slots.TS1 = true
	return dmr.Network{
		Name:       "test",
		Repeaters:  []dmr.Repeater{r},
		Talkgroups: []dmr.Talkgroup{{Name: "FAENA1", ID: "100", Slot: 1}},
		Policy:     dmr.DefaultPolicy(),
	}
}

// withNet swaps radios (and optionally repeaters) into a base network.
func withNet(base dmr.Network, radios []dmr.Radio, repeaters []dmr.Repeater) dmr.Network {
	n := base
	n.Radios = radios
	if repeaters != nil {
		n.Repeaters = repeaters
	}
	return n
}

// links1 builds manual links: margin m to every radio both directions at
// repeater REP-A.
func links1(serving map[string]string, margins map[string]float64) dmr.NetworkLinks {
	links := dmr.NetworkLinks{
		ServingOf: serving,
	}
	for radio, m := range margins {
		links.Uplinks = append(links.Uplinks, dmr.Link{Repeater: "REP-A", Radio: radio, Dir: dmr.Uplink, MarginDB: m + 20, DistanceKm: 2})
		links.Downlinks = append(links.Downlinks, dmr.Link{Repeater: "REP-A", Radio: radio, Dir: dmr.Downlink, MarginDB: m, DistanceKm: 2})
	}
	return links
}

func deliveryFor(t *testing.T, call int, rep Report, radio string) RadioDelivery {
	t.Helper()
	if len(rep.Calls) <= call {
		t.Fatalf("no hay llamada %d en el reporte", call)
	}
	d := rep.Calls[call].Deliveries
	for _, dv := range d {
		if dv.Radio == radio {
			return dv
		}
	}
	t.Fatalf("la llamada %d no registra al radio %q", call, radio)
	return RadioDelivery{}
}

func TestBasicDelivery(t *testing.T) {
	n := withNet(oneRepNet(), []dmr.Radio{
		{Name: "R1", Model: "dp4801e"},
		{Name: "R2", Model: "dp4801e"},
	}, nil)
	links := links1(map[string]string{"R1": "REP-A", "R2": "REP-A"},
		map[string]float64{"R1": +20, "R2": +30})

	rep, err := Run(n, links, Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 1000, DurationMS: 4000},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.Calls) != 1 || rep.Calls[0].Status != "aired" {
		t.Fatalf("estado de la llamada: %+v", rep.Calls[0].Status)
	}
	if got := rep.Calls[0].Site; got != "REP-A" {
		t.Fatalf("sitio: %q", got)
	}

	d1 := deliveryFor(t, 0, rep, "R1")
	if !d1.Delivered || d1.Route != "caller" {
		t.Fatalf("origen: %+v", d1)
	}
	d2 := deliveryFor(t, 0, rep, "R2")
	if !d2.Delivered {
		t.Fatalf("R2 no escucha: %+v", d2)
	}
	if d2.Route != "local" || d2.AtMS != 0 {
		t.Fatalf("R2 ruta/at: %+v", d2)
	}

	// Loads: one charge of 4000 ms over window 6001 ms.
	var load *SiteLoad
	for i := range rep.Loads {
		if rep.Loads[i].Repeater == "REP-A" && rep.Loads[i].Slot == 1 {
			load = &rep.Loads[i]
		}
	}
	if load == nil {
		t.Fatal("no hay carga para REP-A TS1")
	}
	if load.TransmitMS != 4000 {
		t.Fatalf("tx total %d, want 4000", load.TransmitMS)
	}
}

func TestNoMarginRadioNotDelivered(t *testing.T) {
	n := withNet(oneRepNet(), []dmr.Radio{
		{Name: "R1", Model: "dp4801e"},
		{Name: "RLEJOS", Model: "dp4801e"},
	}, nil)
	links := links1(map[string]string{"R1": "REP-A", "RLEJOS": "REP-A"},
		map[string]float64{"R1": +20, "RLEJOS": -15})

	rep, err := Run(n, links, Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 0, DurationMS: 2000},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	d := deliveryFor(t, 0, rep, "RLEJOS")
	if d.Delivered || d.Reason != "no_margin" {
		t.Fatalf("radio con margen negativo: %+v", d)
	}
}

func TestNoPositionRadioExplained(t *testing.T) {
	n := withNet(oneRepNet(), []dmr.Radio{
		{Name: "R1", Model: "dp4801e"},
		{Name: "RSTOCK", Model: "dp4801e"}, // sin posición
	}, nil)
	links := links1(map[string]string{"R1": "REP-A"},
		map[string]float64{"R1": +20})

	rep, err := Run(n, links, Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 0, DurationMS: 2000},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	d := deliveryFor(t, 0, rep, "RSTOCK")
	if d.Delivered || d.Reason != "no_position" {
		t.Fatalf("radio sin posición: %+v", d)
	}
}

func TestTGSubscriptionFilters(t *testing.T) {
	n := withNet(oneRepNet(), []dmr.Radio{
		{Name: "R1", Model: "dp4801e"},
		{Name: "R2", Model: "dp4801e", Talkgroups: []string{"FAENA1"}},
		{Name: "R3", Model: "dp4801e", Talkgroups: []string{"EMERGENCIA"}},
	}, nil)
	n.Talkgroups = append(n.Talkgroups, dmr.Talkgroup{Name: "EMERGENCIA", ID: "911", Slot: 2})
	links := links1(map[string]string{"R1": "REP-A", "R2": "REP-A", "R3": "REP-A"},
		map[string]float64{"R1": +20, "R2": +20, "R3": +20})

	rep, err := Run(n, links, Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 0, DurationMS: 2000},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if d := deliveryFor(t, 0, rep, "R2"); !d.Delivered {
		t.Fatalf("R2 suscrito debería escuchar: %+v", d)
	}
	if d := deliveryFor(t, 0, rep, "R3"); d.Delivered || d.Reason != "tg_no_sub" {
		t.Fatalf("R3 no suscrito: %+v", d)
	}
}

func TestTGClaimCrossSiteNotDuplicated(t *testing.T) {
	n := oneRepNet()
	rB := dmr.Repeater{Name: "REP-B", Model: "slr5500", TXMHz: 454.200, RXMHz: 449.200, TxPowerW: 50, AntennaGainDBi: 3, RxSensitivityDBm: -112, AntennaHeightM: 15}
	rB.Slots.TS1 = true
	rA := n.Repeaters[0]
	rA.IPSCPeers = []string{"REP-B"}
	n.Repeaters = []dmr.Repeater{rA, rB}

	radios := []dmr.Radio{
		{Name: "RA", Model: "dp4801e"},
		{Name: "RB", Model: "dp4801e"},
	}
	n.Radios = radios
	links := dmr.NetworkLinks{
		ServingOf: map[string]string{"RA": "REP-A", "RB": "REP-B"},
	}
	links.Uplinks = []dmr.Link{
		{Repeater: "REP-A", Radio: "RA", Dir: dmr.Uplink, MarginDB: 40, DistanceKm: 2},
		{Repeater: "REP-B", Radio: "RB", Dir: dmr.Uplink, MarginDB: 40, DistanceKm: 2},
	}
	links.Downlinks = []dmr.Link{
		{Repeater: "REP-A", Radio: "RA", Dir: dmr.Downlink, MarginDB: 40, DistanceKm: 2},
		{Repeater: "REP-B", Radio: "RB", Dir: dmr.Downlink, MarginDB: 40, DistanceKm: 2},
	}

	rep, err := Run(n, links, Scenario{Calls: []Call{
		{From: "RA", TG: "FAENA1", AtMS: 0, DurationMS: 10000},
		{From: "RB", TG: "FAENA1", AtMS: 2000, DurationMS: 3000}, // TG ocupado
	}}, true)
	if err != nil {
		t.Fatal(err)
	}

	// RA empuja su llamada y, como TG claim vive en REP-A, REP-B replica:
	dB := deliveryFor(t, 0, rep, "RB")
	if !dB.Delivered || dB.Route != "ipsc" || dB.AtMS != 80 || dB.Hops != 1 {
		t.Fatalf("RB tras la llamada de RA: %+v", dB)
	}

	// La segunda llamada (RB) es local: RA no debe recibirla.
	dA := deliveryFor(t, 1, rep, "RA")
	if dA.Delivered || dA.Reason != "site_not_linked" {
		t.Fatalf("RA tras la llamada duplicada de TG: %+v", dA)
	}
	// RB sí la recibe localmente (y con garble simultáneo propio no
	// existe: slots de sitios distintos).
	if d := deliveryFor(t, 1, rep, "RB"); !d.Delivered {
		t.Fatalf("RB local receipt: %+v", d)
	}
}

func TestDenyPolicyRefusesSecondKeyup(t *testing.T) {
	n := oneRepNet()
	n.Policy.Collision = dmr.CollisionDeny
	n.Radios = []dmr.Radio{
		{Name: "R1", Model: "dp4801e"},
		{Name: "R2", Model: "dp4801e"},
	}
	links := links1(map[string]string{"R1": "REP-A", "R2": "REP-A"},
		map[string]float64{"R1": +20, "R2": +20})

	rep, err := Run(n, links, Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 0, DurationMS: 5000},
		{From: "R2", TG: "FAENA1", AtMS: 1000, DurationMS: 2000},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Calls[1].Status != "denied" {
		t.Fatalf("segunda keyup con deny: %+v", rep.Calls[1].Status)
	}
	if d := deliveryFor(t, 1, rep, "R2"); d.Delivered || d.Reason != "denied_busy" {
		t.Fatalf("entrega de R2 en deny: %+v", d)
	}

	// Airetime solo del primero.
	if got := rep.Loads[0].TransmitMS; got != 5000 {
		t.Fatalf("aire total %d, want 5000", got)
	}
}

func TestOverlapPolicyGarblesBoth(t *testing.T) {
	nR := oneRepNet() // default policy = overlap
	nR.Radios = []dmr.Radio{
		{Name: "R1", Model: "dp4801e"},
		{Name: "R2", Model: "dp4801e"},
		{Name: "R3", Model: "dp4801e"},
	}
	nR.Talkgroups[0].Slot = 0 // slot auto (REP-A TS1)
	links := links1(map[string]string{"R1": "REP-A", "R2": "REP-A", "R3": "REP-A"},
		map[string]float64{"R1": +20, "R2": +20, "R3": +20})

	rep, err := Run(nR, links, Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 0, DurationMS: 6000},
		{From: "R2", TG: "FAENA1", AtMS: 2000, DurationMS: 2000}, // pisando el medio
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if d := deliveryFor(t, 0, rep, "R3"); !d.Delivered || !d.Garbled {
		t.Fatalf("R3 debería escuchar degradado durante el solape: %+v", d)
	}
	if d := deliveryFor(t, 1, rep, "R3"); !d.Delivered || !d.Garbled {
		t.Fatalf("la segunda keyup también degrada a quien escucha: %+v", d)
	}
}

// El guard absorbe solapes más cortos que su ventana (una pisada de
// decenas de ms queda bajo resolución de trama: el interleave del codec
// la traga sin fallo audible). Una pisada larga sí garbiliza — esa sos la
// otra prueba.
func TestGuardWindowAbsorbsShortOverlap(t *testing.T) {
	n := oneRepNet()
	n.Radios = []dmr.Radio{
		{Name: "R1", Model: "dp4801e"},
		{Name: "R2", Model: "dp4801e"},
	}
	links := links1(map[string]string{"R1": "REP-A", "R2": "REP-A"},
		map[string]float64{"R1": +20, "R2": +20})

	rep, err := Run(n, links, Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 0, DurationMS: 6000},
		{From: "R2", TG: "FAENA1", AtMS: 5980, DurationMS: 40}, // solape de ~20 ms
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if d := deliveryFor(t, 0, rep, "R2"); d.Garbled {
		t.Fatalf("solape más corto que el guard no debe degradar: %+v", d)
	}

	// La misma pisada, ahora larga: sí degrada.
	rep, err = Run(n, links, Scenario{Calls: []Call{
		{From: "R1", TG: "FAENA1", AtMS: 0, DurationMS: 6000},
		{From: "R2", TG: "FAENA1", AtMS: 1000, DurationMS: 2000},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if d := deliveryFor(t, 0, rep, "R2"); !d.Garbled {
		t.Fatalf("pisada larga debe degradar: %+v", d)
	}
}

func TestUnknownRadioAndTGGiveErrors(t *testing.T) {
	n := oneRepNet()
	n.Radios = []dmr.Radio{{Name: "R1", Model: "dp4801e"}}
	links := links1(map[string]string{"R1": "REP-A"}, map[string]float64{"R1": +20})

	if _, err := Run(n, links, Scenario{Calls: []Call{{From: "GHOST", TG: "FAENA1", DurationMS: 100}}}, true); err == nil {
		t.Fatal("radio inexistente debería ser error")
	}
	if _, err := Run(n, links, Scenario{Calls: []Call{{From: "R1", TG: "FANTASMA", DurationMS: 100}}}, true); err == nil {
		t.Fatal("talkgroup inexistente debería ser error")
	}
}

func TestFSPLModeIsReported(t *testing.T) {
	n := oneRepNet()
	n.Radios = []dmr.Radio{{Name: "R1", Model: "dp4801e"}}
	links := links1(map[string]string{"R1": "REP-A"}, map[string]float64{"R1": +20})
	rep, err := Run(n, links, Scenario{Calls: []Call{{From: "R1", TG: "FAENA1", DurationMS: 100}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.FSPLMode || len(rep.Notes) == 0 {
		t.Fatalf("aviso de modo sin terreno: %+v", rep)
	}
}
