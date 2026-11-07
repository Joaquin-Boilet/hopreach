package dmr

// Physics references. With the repeater on rising ground, a radio path
// from a 1.6 m handheld is obstruction-free, so the DMR link layer must
// reproduce plain free-space path loss exactly and nothing else.
//
// Why the test terrain is NOT flat: a 1.6 m antenna over flat ground
// genuinely suffers knife-edge loss (Fresnel v lands around −0.4 and ITU-R
// P.526 grants it ~2-3 dB) — real physics, kept out of a test whose job is
// to isolate the link-budget arithmetic. The operating case this mode is
// built for — repeater on a hill, radio in the valley — is what is pinned
// here.

import (
	"math"
	"testing"
)

func floatEq(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func fsplFor(dKm, f float64) float64 {
	return 20*math.Log10(dKm) + 20*math.Log10(f) + 32.44
}

// rampGrid decays the ground linearly from PeakM at the repeater position
// to 0 at SpanKm — the smooth cone a hilltop site sits on. A cone keeps
// the direct line clears everywhere: direct(d) grows as 313·(d/D) while
// the ground decays strictly faster the closer to the peak.
type rampGrid struct {
	RepLat  float64
	RepLon  float64
	PeakM   float64
	SpanKm  float64
	LatStep float64 // km per degree of longitude at this latitude
}

func (g rampGrid) At(lat, lon float64) float64 {
	dKm := math.Hypot((lat-g.RepLat)*111.0, (lon-g.RepLon)*g.LatStep)
	if dKm >= g.SpanKm {
		return 0
	}
	return g.PeakM * (1 - dKm/g.SpanKm)
}

func makeRep() Repeater {
	return Repeater{
		Name: "REP-A", Model: "slr5500",
		Lat: -33.35, Lon: -70.43,
		AntennaHeightM:   15,
		TxPowerW:         50,
		AntennaGainDBi:   3,
		RxSensitivityDBm: -112,
		TXMHz:            454.175,
		RXMHz:            449.175,
	}
}

// radioAt places a radio exactly northKm north of the repeater (the model
// haversine counts ~111 km per degree of latitude).
func radioAt(r Repeater, name string, northKm float64) Radio {
	lat := r.Lat + northKm/111.0
	lon := r.Lon
	return Radio{
		Name:             name,
		Model:            "dp4801e",
		AntennaHeightM:   1.6,
		TxPowerW:         4,
		AntennaGainDBi:   0,
		RxSensitivityDBm: -97,
		Lat:              &lat,
		Lon:              &lon,
	}
}

func tryMargin(t *testing.T, what string, got, want float64) {
	t.Helper()
	if !floatEq(got, want, 0.25) {
		t.Errorf("%s: got %.3f, want %.3f (Δ %.2f)", what, got, want, got-want)
	}
}

func TestComputeLinksHilltopMatchesFSPL(t *testing.T) {
	r := makeRep()
	grid := rampGrid{RepLat: r.Lat, RepLon: r.Lon, PeakM: 300, SpanKm: 1.5, LatStep: 88}
	radio := radioAt(r, "R1", 2.0)

	// Uplink, written from the model's own constants:
	// received = 36.02 dBm (4 W) + 0 (radio TX gain) + 3 (rep RX gain)
	//            − FSPL(2 km, 449.175); margin = received − (−112) − 10
	wantUp := 36.02 + 0 + 3 - fsplFor(2.0, 449.175) + 112 - 10
	up, err := LinkBetween(grid, r, radio, Uplink)
	if err != nil {
		t.Fatal(err)
	}
	tryMargin(t, "uplink 2 km", up.MarginDB, wantUp)

	// Downlink: 46.99 dBm out of the repeater, the radio's −97 receiver.
	wantDown := 46.99 + 3 + 0 - fsplFor(2.0, 454.175) + 97 - 10
	down, err := LinkBetween(grid, r, radio, Downlink)
	if err != nil {
		t.Fatal(err)
	}
	tryMargin(t, "downlink 2 km", down.MarginDB, wantDown)
}

func TestComputeLinksAssociatesByStrongestUplink(t *testing.T) {
	grid := FlatGrid{GroundM: 0}
	rNear := makeRep()
	rFar := makeRep()
	rFar.Name = "REP-B"
	rFar.Lon = rNear.Lon - 0.5 // far away: FSPL loses to the near site

	radio := radioAt(rNear, "R", 10.0) // 10 km north of the NEAR repeater
	n := Network{Name: "t", Repeaters: []Repeater{rNear, rFar}, Radios: []Radio{radio}}
	links, err := ComputeLinks(n, grid)
	if err != nil {
		t.Fatal(err)
	}
	if got := links.HomeOf("R"); got != "REP-A" {
		t.Errorf("home del radio: %q (quería REP-A, el más cercano)", got)
	}
}

func TestLinkParamsAsymmetry(t *testing.T) {
	r := makeRep()
	radio := radioAt(r, "R", 2.0)

	upP := linkParams(Uplink, r, radio)
	if upP.FrequencyMHz != 449.175 || upP.RxSensitivityDB != -112 || upP.TxPowerDBm < 35.9 || upP.TxPowerDBm > 36.2 {
		t.Errorf("params subida: %+v", upP)
	}
	dnP := linkParams(Downlink, r, radio)
	if dnP.FrequencyMHz != 454.175 || dnP.RxSensitivityDB != -97 || dnP.TxPowerDBm < 46.9 || dnP.TxPowerDBm > 47.1 {
		t.Errorf("params bajada: %+v", dnP)
	}
	// RxHeightM belongs to whoever is the RECEIVER: the mast AGL on
	// uplink, the handheld height on downlink.
	if dnP.RxHeightM != 1.6 || upP.RxHeightM != 15 {
		t.Errorf("alturas rx cruzadas: up=%.1f down=%.1f", upP.RxHeightM, dnP.RxHeightM)
	}
}

func TestClassification(t *testing.T) {
	cases := []struct {
		m    float64
		want string
	}{
		{20, "solid"}, {9.1, "solid"}, {9, "solid"},
		{5, "ok"}, {0.1, "ok"}, {0, "ok"},
		{-3, "marginal"}, {-5.9, "marginal"},
		{-6.1, "blocked"}, {-124.5, "blocked"},
	}
	for _, c := range cases {
		if classify(c.m) != c.want {
			t.Errorf("classify(%.1f) = %q, quería %q", c.m, classify(c.m), c.want)
		}
	}
}

func TestNetworkParsingDefaultsAndErrors(t *testing.T) {
	// Presets fill what the file leaves blank.
	n, err := ParseYAML([]byte(`
network_name: "prueba"
repeaters:
  - name: REP
    tx_freq_mhz: 454.175
    rx_freq_mhz: 449.175
radios:
  - name: R1
    lat: -33.4
    lon: -70.6
talkgroups:
  - name: FAENA1
    id: "100"
    slot: 1
`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Repeaters[0].Model != "slr5500" || n.Repeaters[0].TxPowerW != 50 || n.Repeaters[0].AntennaHeightM != 15 {
		t.Errorf("preset de repetidora no aplicado: %+v", n.Repeaters[0])
	}
	if n.Radios[0].Model != "dp4801e" || n.Radios[0].Role != "handy" || n.Radios[0].TxPowerW != 4 {
		t.Errorf("preset de radio no aplicado: %+v", n.Radios[0])
	}
	if n.Policy.Collision != CollisionOverlap || n.Policy.IPSCLatencyMs != 80 {
		t.Errorf("política por defecto: %+v", n.Policy)
	}

	// Un modelo desconocido es error, no un fallback silencioso.
	if _, err := ParseYAML([]byte("repeaters:\n  - name: X\n    model: noexiste\n    tx_freq_mhz: 450\n    rx_freq_mhz: 455\n")); err == nil {
		t.Fatal("modelo desconocido debería ser error")
	}

	// Peer inexistente también.
	if _, err := ParseYAML([]byte("repeaters:\n  - name: A\n    tx_freq_mhz: 450\n    rx_freq_mhz: 455\n    peers: [FANTASMA]\n")); err == nil {
		t.Fatal("peer inexistente debería ser error")
	}
}
