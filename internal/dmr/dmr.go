// Package dmr models a DMR MotoTRBO radio network for HopReach-DMR: the
// repeaters, the handhelds (handys) and the talkgroups, plus the RF link
// budget between every handheld and every repeater computed over real
// terrain with the same physics the rest of HopReach trusts
// (internal/propagation).
//
// The model is deliberately Tier II conventional first: one channel per
// site (a frequency pair TX/RX), two TDMA slots, handhelds transmit on the
// input frequency and listen on the output frequency, and sites linked by
// IP Site Connect (IPSC peers) replicate traffic across the WAN. Tier III
// trunking is a different controller-shaped problem and is not claimed
// here — see docs/DMR_MODE.md for what is and is not modelled.
//
// Every numeric device parameter carries an editable YAML override: the
// built-in values are Motorola's published-class tradeoff figures — not a
// statement about anyone's particular installation — and the file is the
// source of truth for an operator's fleet.
package dmr

import (
	"fmt"
	"math"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// RepeaterPreset is one catalogue entry: a repeater hardware family with
// its usual field values. Every value is an override suggestion, not a
// specification — real deployments differ (filtering, antenna system,
// battery discipline).
type RepeaterPreset struct {
	TxPowerW         float64
	AntennaGainDBi   float64 // omni/grand-angulaire baseline, site-tuned in practice
	RxSensitivityDBm float64 // DMR BER 5% reference receiver spec
	AntennaHeightM   float64 // typical mast height suggestion
}

// RadioPreset is one catalogue entry for a portable/mobile family.
type RadioPreset struct {
	TxPowerW         float64
	AntennaGainDBi   float64
	RxSensitivityDBm float64 // DMR BER 5% reference
	HeightM          float64 // antenna height above hand level, portable
}

// repeaterPresets is the repeater catalogue. Sources: Motorola data
// sheets (SLR5500 UHF 50 W class; XPR8300e 40 W; SLR1000 45 W) rounded to
// the class the model actually ships in, with a conservative published
// receiver figure. Symbolic DMR numbers rather than marketing "static
// coverage" curves — deliberately a *couple dB conservative* so planning
// errs on the safe side.
var repeaterPresets = map[string]RepeaterPreset{
	"slr5500":  {TxPowerW: 50, AntennaGainDBi: 3.0, RxSensitivityDBm: -112, AntennaHeightM: 15},
	"slr1000":  {TxPowerW: 45, AntennaGainDBi: 3.0, RxSensitivityDBm: -112, AntennaHeightM: 12},
	"xpr8300e": {TxPowerW: 40, AntennaGainDBi: 3.0, RxSensitivityDBm: -110, AntennaHeightM: 12},
}

// radioPresets is the portable/mobile catalogue (motoTRBO family).
var radioPresets = map[string]RadioPreset{
	"dp4801e": {TxPowerW: 4, AntennaGainDBi: 0.0, RxSensitivityDBm: -97, HeightM: 1.6},
	"dp4401e": {TxPowerW: 4, AntennaGainDBi: 0.0, RxSensitivityDBm: -97, HeightM: 1.6},
	"sl7550e": {TxPowerW: 2, AntennaGainDBi: 0.0, RxSensitivityDBm: -95, HeightM: 1.6},
	"r7a":     {TxPowerW: 5, AntennaGainDBi: 0.0, RxSensitivityDBm: -97, HeightM: 1.6},
	"dm4601e": {TxPowerW: 45, AntennaGainDBi: 3.0, RxSensitivityDBm: -97, HeightM: 2.4},
}

// Repeater is one DMR repeater site.
type Repeater struct {
	Name  string  `yaml:"name"`
	DMRID uint32  `yaml:"dmr_id"`
	Model string  `yaml:"model"` // catalogue key; empty → "slr5500"
	Lat   float64 `yaml:"lat"`
	Lon   float64 `yaml:"lon"`

	// Overrides — empty/zero means "take from the preset".
	AntennaHeightM   float64 `yaml:"antenna_height_m"` // AGL above ground
	TxPowerW         float64 `yaml:"tx_power_w"`
	AntennaGainDBi   float64 `yaml:"antenna_gain_dbi"`
	RxSensitivityDBm float64 `yaml:"rx_sensitivity_dbm,flow"`

	// The duplex pair this site operates on: TXMHz is the *downlink* (the
	// repeater transmits here; handys listen), RXMHz is the *uplink* (the
	// handys transmit here; the repeater listens). Both are required —
	// this model is duplex, not a single-frequency configuration.
	TXMHz float64 `yaml:"tx_freq_mhz"`
	RXMHz float64 `yaml:"rx_freq_mhz"`

	// IPSCPeers lists the other repeater names this site is linked to via
	// IP Site Connect. One-directional declarations are normalised: if A
	// lists B, the link A↔B exists whether or not B lists A back.
	IPSCPeers []string `yaml:"peers"`

	// Slots enables each TDMA timeslot. Default both enabled.
	Slots Slots `yaml:"slots"`

	// Scope is the map/region tag this site belongs to (mirrors MeshCore's
	// `default_scope` slot so the existing pipeline + map filters work
	// unchanged). Optional.
	Scope string `yaml:"scope"`
}

// Slots sets which TDMA timeslots a repeater carries.
type Slots struct {
	TS1 bool `yaml:"ts1"`
	TS2 bool `yaml:"ts2"`
}

// Enabled reports whether slot 1 or 2 is carried.
func (s Slots) Enabled(slot int) bool {
	if slot == 1 {
		return s.TS1
	}
	return s.TS2
}

// Radio is one DMR device that is not a repeater: a handheld (portable)
// or a mobile. Position is required for simulation and optional for the
// roster (a radio without position still shows in lists but contributes
// no coverage and no links).
type Radio struct {
	Name  string   `yaml:"name"`
	Role  string   `yaml:"role"` // "handy" (default) or "mobile"
	Model string   `yaml:"model"`
	Lat   *float64 `yaml:"lat"`
	Lon   *float64 `yaml:"lon"`

	AntennaHeightM   float64 `yaml:"height_m"`
	TxPowerW         float64 `yaml:"tx_power_w"`
	AntennaGainDBi   float64 `yaml:"antenna_gain_dbi"`
	RxSensitivityDBm float64 `yaml:"rx_sensitivity_dbm"`

	// Talkgroups the radio is subscribed to. Empty = carries everything on
	// its site (defensible for a service radio; operators usually narrow
	// this in reality — it is why the field exists).
	Talkgroups []string `yaml:"tgs"`

	// Notes keeps free text (owner, serial, whatever) out of the way.
	Notes string `yaml:"notes"`
}

// Talkgroup is a named DMR group address. Conventional Tier II pin each
// talkgroup to a timeslot so collision bookkeeping stays honest.
type Talkgroup struct {
	Name string `yaml:"name" json:"name"` // internal short name, e.g. "FAENA1"
	ID   string `yaml:"id" json:"id"`     // DMR numeric TG id, e.g. "100"
	Slot int    `yaml:"slot" json:"slot"` // 1 or 2; 0 = any (first enabled slot)
}

// Network is the whole topology file's shape.
type Network struct {
	Name       string      `yaml:"network_name"`
	Comment    string      `yaml:"comment"`
	Repeaters  []Repeater  `yaml:"repeaters"`
	Radios     []Radio     `yaml:"radios"`
	Talkgroups []Talkgroup `yaml:"talkgroups"`

	// Policy is the simulator's shared plausibility knobs (see dmr.Policy).
	Policy Policy `yaml:"sim_policy"`
}

// Policy groups the DMR-layer behavioural assumptions the simulator uses.
// Defaults live in DefaultPolicy, not zero values — a policy absent from
// the file means "the sensible defaults below", never "nothing applies".
type Policy struct {
	// Collision is what happens when a second PTT keys a slot that is
	// already carrying traffic on the same site:
	//   - "overlap" (default): both transmissions air and garble each
	//     other for the overlap interval — the un-coordinated Tier II
	//     reality a fleet sees when nobody programmed TX restrictions.
	//   - "deny": the second keyup is refused (the radio shows the busy
	//     tone and does not transmit), as on sites configured with slot
	//     resumption / PTT controls.
	Collision string `yaml:"collision" json:"collision"`

	// IPSCLatencyMs is the per-hop replication delay assumed for IP
	// Site Connect: how long between the repeating site receiving audio
	// and a peer site transmitting it downlink. 40-90 ms covers real
	// deployments; 80 is the midpoint default.
	IPSCLatencyMs int `yaml:"ipsc_latency_ms" json:"ipsc_latency_ms"`

	// GuardMS is the bracket around a PTT edge inside which an overlapping
	// keyup counts as "contention": DMR frames are 30 ms and radio voice
	// bursts don't sub-frame align perfectly with each other, so a second
	// PTT landing within this many ms of a continuous call does not
	// destroy the first speaker's audio — it is the classic "stepped-in
	// during the hangover" quietly absorbed by the codec's interleaving.
	GuardMS int `yaml:"guard_ms" json:"guard_ms"`
}

const (
	CollisionOverlap = "overlap"
	CollisionDeny    = "deny"
)

// DefaultPolicy is the policy for a topology that won't spell one out.
func DefaultPolicy() Policy {
	return Policy{
		Collision:     CollisionOverlap,
		IPSCLatencyMs: 80,
		GuardMS:       60,
	}
}

// effectiveRepeaterPreset resolves a repeater's preset, applying its own
// overrides on top. Error surfaces a typo'd model name rather than
// silently inventing a device.
func effectiveRepeater(r Repeater) (Repeater, error) {
	model := r.Model
	if model == "" {
		model = "slr5500"
	}
	p, ok := repeaterPresets[strings.ToLower(model)]
	if !ok {
		return r, fmt.Errorf("dmr: repeater %q: unknown model preset %q (have: slr5500, slr1000, xpr8300e)", r.Name, r.Model)
	}
	if r.AntennaHeightM == 0 {
		r.AntennaHeightM = p.AntennaHeightM
	}
	if r.TxPowerW == 0 {
		r.TxPowerW = p.TxPowerW
	}
	if r.AntennaGainDBi == 0 {
		r.AntennaGainDBi = p.AntennaGainDBi
	}
	if r.RxSensitivityDBm == 0 {
		r.RxSensitivityDBm = p.RxSensitivityDBm
	}
	r.Model = model
	return r, nil
}

// effectiveRadioPreset resolves a radio's preset with overrides.
func effectiveRadio(rd Radio) (Radio, error) {
	model := rd.Model
	if model == "" {
		model = "dp4801e"
	}
	p, ok := radioPresets[strings.ToLower(model)]
	if !ok {
		return rd, fmt.Errorf("dmr: radio %q: unknown model preset %q (have: dp4801e, dp4401e, sl7550e, r7a, dm4601e)", rd.Name, rd.Model)
	}
	if rd.Role == "" {
		rd.Role = "handy"
	}
	if rd.AntennaHeightM == 0 {
		rd.AntennaHeightM = p.HeightM
	}
	if rd.TxPowerW == 0 {
		rd.TxPowerW = p.TxPowerW
	}
	if rd.AntennaGainDBi == 0 {
		rd.AntennaGainDBi = p.AntennaGainDBi
	}
	if rd.RxSensitivityDBm == 0 {
		rd.RxSensitivityDBm = p.RxSensitivityDBm
	}
	rd.Model = model
	return rd, nil
}

// Effective resolves every preset and validates the network's invariants.
// It returns a copy — callers can trust it fully resolved without mutating
// the parsed inputs.
func (n Network) Effective() (Network, error) {
	if n.Name == "" {
		n.Name = "red DMR"
	}
	if len(n.Repeaters) == 0 {
		return n, fmt.Errorf("dmr: la red no declara repetidoras: no hay topología que simular")
	}

	byName := make(map[string]bool, len(n.Repeaters))
	for i := range n.Repeaters {
		r := n.Repeaters[i]
		if strings.TrimSpace(r.Name) == "" {
			return n, fmt.Errorf("dmr: repetidora %d sin nombre", i+1)
		}
		if byName[r.Name] {
			return n, fmt.Errorf("dmr: nombre de repetidora duplicado %q", r.Name)
		}
		byName[r.Name] = true
	}

	for i := range n.Repeaters {
		r, err := effectiveRepeater(n.Repeaters[i])
		if err != nil {
			return n, err
		}
		if r.TXMHz <= 0 || r.RXMHz <= 0 {
			return n, fmt.Errorf("dmr: repetidora %q sin par de frecuencias (tx_freq_mhz/rx_freq_mhz)", r.Name)
		}
		if !r.Slots.TS1 && !r.Slots.TS2 {
			r.Slots.TS1 = true
		}
		for _, peer := range r.IPSCPeers {
			if !byName[peer] {
				return n, fmt.Errorf("dmr: repetidora %q declara peer %q que no existe", r.Name, peer)
			}
		}
		n.Repeaters[i] = r
	}

	for i := range n.Radios {
		rd, err := effectiveRadio(n.Radios[i])
		if err != nil {
			return n, err
		}
		n.Radios[i] = rd
	}

	for i, tg := range n.Talkgroups {
		if strings.TrimSpace(tg.Name) == "" {
			return n, fmt.Errorf("dmr: talkgroup %d sin nombre", i+1)
		}
		if strings.TrimSpace(tg.ID) == "" {
			return n, fmt.Errorf("dmr: talkgroup %q sin id numérico", tg.Name)
		}
	}

	if n.Policy.Collision == "" {
		n.Policy.Collision = CollisionOverlap
	}
	if n.Policy.IPSCLatencyMs == 0 {
		n.Policy.IPSCLatencyMs = DefaultPolicy().IPSCLatencyMs
	}
	if n.Policy.GuardMS == 0 {
		n.Policy.GuardMS = DefaultPolicy().GuardMS
	}
	if n.Policy.Collision != CollisionOverlap && n.Policy.Collision != CollisionDeny {
		return n, fmt.Errorf("dmr: sim_policy.collision %q inválido (overlap|deny)", n.Policy.Collision)
	}
	return n, nil
}

// RepeaterByName returns the repeater with the given name (Effective must
// have been called first; it guarantees names are unique).
func (n Network) RepeaterByName(name string) *Repeater {
	for i := range n.Repeaters {
		if n.Repeaters[i].Name == name {
			return &n.Repeaters[i]
		}
	}
	return nil
}

// LoadYAML reads and resolves a topology file. A missing file is an error:
// unlike config.yaml, a topology has no useful zero value — an empty
// fallback would silently simulate the wrong network.
func LoadYAML(path string) (Network, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Network{}, fmt.Errorf("dmr: leyendo topología %s: %w", path, err)
	}
	return ParseYAML(body)
}

// ParseYAML parses and resolves topology bytes.
func ParseYAML(body []byte) (Network, error) {
	var n Network
	if err := yaml.Unmarshal(body, &n); err != nil {
		return Network{}, fmt.Errorf("dmr: parseando topología YAML: %w", err)
	}
	return n.Effective()
}

// TalkgroupSlot returns which slot a talkgroup runs on for a given
// repeater: the TG's own declared slot if explicit, else the repeater's
// first enabled slot.
func TalkgroupSlot(tg Talkgroup, r Repeater) int {
	if tg.Slot == 1 || tg.Slot == 2 {
		return tg.Slot
	}
	if r.Slots.TS1 {
		return 1
	}
	return 2
}

// dbmFromWatts converts transmit power to dBm: dBW + 30, since 1 W is
// 10*log10(1000 / 1 mW) = 30 dBm.
func dbmFromWatts(w float64) float64 {
	return 10*math.Log10(w) + 30
}
