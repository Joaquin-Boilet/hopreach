// Links between radios and repeaters are the raw material of both the
// topology map's colouring and the simulator's eligibility rules: a link
// exists when the receiving side's margin is positive (it decodes), and
// degrades quantitatively with the dB margin itself.
//
// Both directions are computed independently and are NOT symmetrised: the
// classic MotoTRBO asymmetry — a hilltop repeater hears a handheld much
// further than the handheld hears that repeater's downlink — is the rule,
// not the exception, and collapsing the two into one number makes the
// model wrong in exactly the direction operators notice first ("why
// doesn't my radio hear the call nobody else missed").
package dmr

import (
	"fmt"
	"math"

	"hopreach/internal/propagation"
)

// earthRadiusM and refractionK duplicate propagation's private Earth
// geometry so a self-contained worst-obstruction walk can report the same
// v the real model used — the physics constants, not domain logic (the
// math itself delegates to propagation.KnifeEdgeDiffractionDB).
const (
	_earthRadiusM = 6371.0088 * 1000
	_refractionK  = 4.0 / 3.0
)

// FlatGrid is a constant-elevation elevation lookup. It exists for
// deterministic tests and for the CLI's explicit free-space mode; the
// simulator surfaces that its results were terrain-free rather than
// implying terrain was modelled.
type FlatGrid struct{ GroundM float64 }

func (g FlatGrid) At(lat, lon float64) float64 { return g.GroundM }

// LinkDirection is which way the link budget runs.
type LinkDirection int

const (
	// Uplink: the radio transmits, the repeater receives (on RXMHz).
	Uplink LinkDirection = iota
	// Downlink: the repeater transmits, the radio receives (on TXMHz).
	Downlink
)

// Link is one computed radio↔repeater pair in one direction.
type Link struct {
	Repeater string
	Radio    string
	Dir      LinkDirection

	// MarginDB is received_level − sensitivity − fade_margin. Positive
	// decodes. This is the number everything else in the DMR mode is
	// built from.
	MarginDB float64

	// PathDB is the total path loss the model credited (FSPL plus the
	// worst knife-edge diffraction along the real terrain path), kept for
	// the UI's tooltip ("what the hill cost").
	PathDB float64

	DistanceKm float64
}

// Class labels a link for the UI and reports.
// >= +9 dB solid, >= 0 usable, >= −6 marginal, below that blocked.
func (l Link) Class() string { return classify(l.MarginDB) }

func classify(margin float64) string {
	switch {
	case margin >= 9:
		return "solid"
	case margin >= 0:
		return "ok"
	case margin >= -6:
		return "marginal"
	default:
		return "blocked"
	}
}

// linkParams builds propagation.Params for one direction of one link.
// Whoever transmits contributes power and TX gain; whoever receives
// contributes RX gain (~their antenna) and their own sensitivity. Fade
// margin is a fixed 10 dB — the operating-case insurance against
// multipath and device drift that any LMR deployment relies on.
func linkParams(dir LinkDirection, r Repeater, rd Radio) propagation.Params {
	txPowerDBm := dbmFromWatts(r.TxPowerW)
	txGain := r.AntennaGainDBi
	rxGain := rd.AntennaGainDBi
	rxSens := rd.RxSensitivityDBm
	rxHeightM := rd.AntennaHeightM // above the receiver's own ground
	freqMHz := r.TXMHz             // repeater transmits on its TX frequency

	if dir == Uplink {
		txPowerDBm = dbmFromWatts(rd.TxPowerW)
		txGain = rd.AntennaGainDBi
		rxGain = r.AntennaGainDBi
		rxSens = r.RxSensitivityDBm
		rxHeightM = r.AntennaHeightM // the repeater's mast, AGL
		freqMHz = r.RXMHz            // the radio transmits on the repeater's RX frequency
	}

	return propagation.Params{
		FrequencyMHz:    freqMHz,
		TxPowerDBm:      txPowerDBm,
		TxAntennaGainDB: txGain,
		RxAntennaGainDB: rxGain,
		RxSensitivityDB: rxSens,
		FadeMarginDB:    10,
		AntennaHeightM:  txAntennaHeightLeftFor(r, rd, dir), // unused by PathMargin itself
		RxHeightM:       rxHeightM,
		MaxRangeKm:      0, // no hard cap: terrain decides
		MarginGreenDB:   9,
	}
}

// txAntennaHeightLeftFor keeps the total params struct filled for callers
// that want to mirror it (like the raster pipeline does; PathMargin itself
// takes the TX height explicitly).
func txAntennaHeightLeftFor(r Repeater, rd Radio, dir LinkDirection) float64 {
	p := r.AntennaHeightM
	if dir == Uplink {
		p = rd.AntennaHeightM
	}
	return p
}

// bestV walks the same great-circle path propagation.PathMargin walks, and
// returns the worst Fresnel-Kirchhoff v and the free-space loss along it,
// so the report can decompose "how the dB was lost" the same way the model
// spends it.
func bestV(grid propagation.Grid, p propagation.Params, txLat, txLon, txHeightASL, rxLat, rxLon, distanceKm float64) (maxV, fspl float64) {
	wavelengthM := 299792458.0 / (p.FrequencyMHz * 1e6)
	dM := math.Max(distanceKm*1000, 100)
	samples := int(distanceKm / 0.05)
	if samples < 16 {
		samples = 16
	}
	if samples > 512 {
		samples = 512
	}
	rxHeightASL := grid.At(rxLat, rxLon) + p.RxHeightM

	maxV = math.Inf(-1)
	for i := 1; i < samples; i++ {
		frac := float64(i) / float64(samples)
		lat := txLat + (rxLat-txLat)*frac
		lon := txLon + (rxLon-txLon)*frac

		d1 := dM * frac
		d2 := dM - d1
		terrain := grid.At(lat, lon)
		bulge := (d1 * d2) / (2 * _refractionK * _earthRadiusM)
		direct := txHeightASL + (rxHeightASL-txHeightASL)*frac
		v := (terrain + bulge - direct) * math.Sqrt((2/wavelengthM)*(1/d1+1/d2))
		if v > maxV {
			maxV = v
		}
	}
	return maxV, propagation.FsplDB(distanceKm, p.FrequencyMHz)
}

// LinkBetween computes one direction of one radio↔repeater link. grid may
// be nil only when the caller has explicitly chosen the free-space mode:
// without terrain the knife-edge term simply never offsets the budget, and
// it is the caller's job to say so in its reports.
func LinkBetween(grid propagation.Grid, r Repeater, rd Radio, dir LinkDirection) (Link, error) {
	if rd.Lat == nil || rd.Lon == nil {
		return Link{}, fmt.Errorf("dmr: radio %q sin posición: no participa en enlaces", rd.Name)
	}

	p := linkParams(dir, r, rd)
	distKm := propagation.HaversineKm(r.Lat, r.Lon, *rd.Lat, *rd.Lon)

	// TX endpoint height ASL: its own ground elevation plus antenna.
	// RX endpoint is lifted inside PathMargin by p.RxHeightM above ITS
	// ground (linkParams put the right radio/mast height there).
	txLat, txLon, rxLat, rxLon := r.Lat, r.Lon, *rd.Lat, *rd.Lon
	if dir == Uplink {
		txLat, txLon, rxLat, rxLon = *rd.Lat, *rd.Lon, r.Lat, r.Lon
	}
	txHeightASL := gridGroundHeight(grid, txLat, txLon, p.AntennaHeightM)

	var margin, pathLoss float64
	if grid == nil {
		// Free-space only: no obstruction term, by explicit caller choice.
		pathLoss = propagation.FsplDB(distKm, p.FrequencyMHz)
		margin = p.TxPowerDBm + p.TxAntennaGainDB + p.RxAntennaGainDB -
			pathLoss - p.RxSensitivityDB - p.FadeMarginDB
	} else {
		margin = propagation.PathMargin(grid, p, txLat, txLon, txHeightASL, rxLat, rxLon, distKm)
		if v, fspl := bestV(grid, p, txLat, txLon, txHeightASL, rxLat, rxLon, distKm); v > -0.78 {
			pathLoss = fspl + propagation.KnifeEdgeDiffractionDB(v)
		} else {
			pathLoss = fspl
		}
	}

	return Link{
		Repeater:   r.Name,
		Radio:      rd.Name,
		Dir:        dir,
		MarginDB:   margin,
		PathDB:     pathLoss,
		DistanceKm: distKm,
	}, nil
}

// gridGroundHeight reads ground elevation for a point; a nil grid is
// treated as sea level (the free-space mode's consistent fiction).
func gridGroundHeight(grid propagation.Grid, lat, lon, antHeight float64) float64 {
	if grid == nil {
		return antHeight
	}
	return grid.At(lat, lon) + antHeight
}

// NetworkLinks is every computed link pair, in both directions.
type NetworkLinks struct {
	Uplinks   []Link `json:"uplinks"`   // radio -> repeater
	Downlinks []Link `json:"downlinks"` // repeater -> radio

	// ServingOf maps radio name -> repeater name: the strongest uplink,
	// which is whose slot the radio keys in a Tier II fleet.
	ServingOf map[string]string `json:"serving_of"`
}

// ComputeLinks walks every positioned radio against every repeater. grid
// nil means the free-space mode (see LinkBetween); an FSPL-only result set
// is still fully computable — it just lies optimistically, and reports
// that they lie.
func ComputeLinks(n Network, grid propagation.Grid) (NetworkLinks, error) {
	out := NetworkLinks{ServingOf: map[string]string{}}
	for _, rd := range n.Radios {
		if rd.Lat == nil || rd.Lon == nil {
			continue
		}
		var bestUp float64 = math.Inf(-1)
		bestRep := ""
		for _, r := range n.Repeaters {
			up, err := LinkBetween(grid, r, rd, Uplink)
			if err != nil {
				return NetworkLinks{}, err
			}
			out.Uplinks = append(out.Uplinks, up)

			dn, err := LinkBetween(grid, r, rd, Downlink)
			if err != nil {
				return NetworkLinks{}, err
			}
			out.Downlinks = append(out.Downlinks, dn)

			// The home site is the strongest uplink, period. It is NOT a
			// viability threshold: a radio in a cul-de-sac valley with
			// every margin negative still keys a (likely blocked-class,
			// likely unheard) site, and the engine/report surfaces that as
			// a no_uplink outcome instead of pretending the radio is
			// homeless. Starting the scan at 0 used to erase exactly the
			// "all margins negative" case — the interesting one on real
			// mountainous terrain.
			if up.MarginDB > bestUp {
				bestUp = up.MarginDB
				bestRep = r.Name
			}
		}
		if bestRep != "" {
			out.ServingOf[rd.Name] = bestRep
		}
	}
	return out, nil
}

// ServingOf returns the strongest-uplink repeater for a radio (its home
// site; empty when the radio has no links). HomeOf names it explicitly so
// it stays a method on the links map, never shadowed by the field.
func (l NetworkLinks) HomeOf(radio string) string {
	return l.ServingOf[radio]
}

// BestMargin documents intent at reading time; nothing else in the mode
// silently picks "the best" without carrying the whole set around.
func BestMargin(links []Link) (Link, bool) {
	best := math.Inf(-1)
	var out Link
	have := false
	for _, l := range links {
		if l.MarginDB > best {
			best = l.MarginDB
			out = l
			have = true
		}
	}
	return out, have
}
