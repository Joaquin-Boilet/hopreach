// The engine turns (topology, terrain links, scenario) into a delivery
// report. It models DMR Tier II + IPSC with these rules — each one a
// documented simplification keyed on what actually decides whether an end
// radio hears a call:
//
//  1. A radio keys its home slot: the (repeater, slot) pair of its
//     strongest-uplink repeater (overridable per call via Site).
//  2. Slot occupancy is exclusive: one repeated call per (repeater,
//     slot). With the "overlap" policy (default) a second keyup airs
//     anyway and both calls garble each other for the overlap — the
//     un-coordinated Tier II reality; with "deny" the second keyup is
//     refused and the radio shows the busy tone.
//  3. Overlaps shorter than the guard window (default 60 ms) are
//     absorbed — below frame resolution the codec's interleaving swallows
//     the sub-frame hit without audible failure; longer overlaps garble
//     BOTH calls for the overlap portion, retro-marking the earlier
//     call's listeners as well.
//  4. Talkgroup claim: across the IPSC network one audio source holds a
//     TG at a time. A call on an already-carried TG still airs locally,
//     but is not replicated cross-site — remote sites keep repeating the
//     older claim. This is the observable "double call on one TG" case.
//  5. Replication: a call is heard at another site only when an IPSC peer
//     path exists to it, costing ipsc_latency_ms per hop (default 80 ms).
//  6. Reception eligibility, in order: it's not the caller; the radio is
//     positioned; its home site carries the call (direct or via IPSC);
//     its downlink margin there is positive (the same number the topology
//     map colours with); and it is subscribed to the TG (empty list =
//     carries everything).
//
// Deliberately NOT modelled: Tier III trunking, private calls, hangover
// timers, frame-level CSBK collisions, duty-cycle caps (reported, not
// enforced), and per-site channel reuse. docs/DMR_MODE.md carries the
// full "limits" list so nobody reads more fidelity into this than it has.
package dmrsim

import (
	"fmt"
	"sort"

	"hopreach/internal/dmr"
)

// RadioDelivery is one radio's outcome for one call.
type RadioDelivery struct {
	Radio string `json:"radio"`

	Delivered bool `json:"delivered"`
	AtMS      int  `json:"at_ms"` // ms after the PTT was keyed

	// Route for a delivered radio: "caller" (it originated), "local"
	// (its own site's repeater repeated it), or "ipsc".
	Route string `json:"route,omitempty"`
	Hops  int    `json:"hops,omitempty"`

	// Reason is why it was NOT delivered: "caller" never matches below
	// (handled separately); "no_position" (radio has no coordinates, so
	// no links), "site_not_linked" (its home site ends nowhere with an
	// IPSC path), "no_margin" (can't decode its home site's downlink),
	// "tg_no_sub" (doesn't carry this talkgroup), "denied_busy" (the
	// whole call was refused under the deny policy).
	Reason string `json:"reason,omitempty"`

	// Garbled marks that during part of the call its (site, slot) carried
	// competing audio — the overlap portion arrives corrupted even when
	// the call still ends delivered.
	Garbled bool `json:"garbled,omitempty"`
}

// CallResult is one call's whole outcome.
type CallResult struct {
	ID     int    `json:"id"`
	From   string `json:"from"`
	TG     string `json:"tg"`
	Site   string `json:"site"`
	Slot   int    `json:"slot"`
	AtMS   int    `json:"at_ms"`
	EndMS  int    `json:"end_ms"`
	Status string `json:"status"` // "aired" | "denied"

	// Garbled is true when the call's (site, slot) carried competing
	// audio at some point: the overlap portion arrived corrupted to every
	// listener of the carrier, whether they heard this call locally or
	// through IPSC.
	Garbled    bool            `json:"garbled,omitempty"`
	Deliveries []RadioDelivery `json:"deliveries"`
}

// SiteLoad is one (site, slot)'s airtime load during the scenario window.
type SiteLoad struct {
	Repeater   string  `json:"repeater"`
	Slot       int     `json:"slot"`
	TransmitMS int     `json:"transmit_ms"`
	DutyCycle  float64 `json:"duty_cycle"`
	Collisions int     `json:"collisions"`
}

// Report is one run's full output.
type Report struct {
	NetworkName string       `json:"network_name"`
	WindowMS    int          `json:"window_ms"`
	Calls       []CallResult `json:"calls"`
	Loads       []SiteLoad   `json:"site_loads"`
	// FSPLMode is true when terrain was NOT modelled: every margin in this
	// report is free-space only and optimistically long.
	FSPLMode bool     `json:"fspl_mode"`
	Notes    []string `json:"notes,omitempty"`
}

type engineState struct {
	network dmr.Network
	links   dmr.NetworkLinks
	st      Scenario

	tgByName map[string]dmr.Talkgroup
	uplink   map[string]map[string]float64 // site -> radio -> margin (caller's keyup)
	downlink map[string]map[string]float64 // site -> radio -> margin
	peersAdj map[string][]string
}

// Run executes the scenario over the resolved network + computed links.
// gridPresent only records whether terrain was actually modelled — it is
// reporting state, not behaviour.
func Run(n dmr.Network, links dmr.NetworkLinks, s Scenario, gridPresent bool) (Report, error) {
	if err := s.Validate(); err != nil {
		return Report{}, err
	}

	st := engineState{
		network:  n,
		links:    links,
		st:       s,
		tgByName: map[string]dmr.Talkgroup{},
		uplink:   map[string]map[string]float64{},
		downlink: map[string]map[string]float64{},
		peersAdj: map[string][]string{},
	}

	for _, tg := range n.Talkgroups {
		st.tgByName[tg.Name] = tg
	}
	for i := range n.Repeaters {
		st.uplink[n.Repeaters[i].Name] = map[string]float64{}
		st.downlink[n.Repeaters[i].Name] = map[string]float64{}
	}
	for _, l := range links.Uplinks {
		st.uplink[l.Repeater][l.Radio] = l.MarginDB
	}
	for _, l := range links.Downlinks {
		st.downlink[l.Repeater][l.Radio] = l.MarginDB
	}
	for i := range n.Repeaters {
		r := n.Repeaters[i]
		for _, p := range r.IPSCPeers {
			st.peersAdj[r.Name] = appendUnique(st.peersAdj[r.Name], p)
			st.peersAdj[p] = appendUnique(st.peersAdj[p], r.Name)
		}
	}

	window := scenarioWindow(s)
	rep := Report{
		NetworkName: n.Name,
		WindowMS:    window,
		Notes:       []string{},
	}
	if !gridPresent {
		rep.FSPLMode = true
		rep.Notes = append(rep.Notes,
			"modo sin terreno: márgenes solo con espacio libre (FSPL); los alcances son optimistas")
	}

	ledger := newTxLedger()

	// Deterministic execution order: keyed time, then scenario order.
	sorted := append([]Call(nil), s.Calls...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].AtMS != sorted[j].AtMS {
			return sorted[i].AtMS < sorted[j].AtMS
		}
		return false
	})

	for i, c := range sorted {
		res, err := simulateCall(st, ledger, c, i+1)
		if err != nil {
			return Report{}, err
		}
		rep.Calls = append(rep.Calls, res)
	}
	rep.Loads = ledger.loads(window)

	markMutualGarble(rep.Calls, n.Policy.GuardMS)
	return rep, nil
}

// markMutualGarble propagates overlap damage to BOTH competing calls and
// to every delivered listener whose home site carried the garble. A pair
// garbles when the later keyup (already in replay order) survives past the
// guard window inside the earlier call's airtime — the same arithmetic the
// sequential deny/overlap decision used, so "denied" and "garbled" never
// disagree about the same pair.
func markMutualGarble(calls []CallResult, guard int) {
	for i := 1; i < len(calls); i++ {
		l := calls[i]
		if l.Status != "aired" {
			continue
		}
		for j := 0; j < i; j++ {
			e := &calls[j]
			if e.Status != "aired" || e.Site != l.Site || e.Slot != l.Slot {
				continue
			}
			// L's shrunk interval [start+g, end-g] against E's owned time.
			if l.AtMS+guard < e.EndMS && l.EndMS-guard > e.AtMS {
				l.Garbled = true
				e.Garbled = true
			}
		}
	}
	for i := range calls {
		if !calls[i].Garbled {
			continue
		}
		for d := range calls[i].Deliveries {
			rd := &calls[i].Deliveries[d]
			if rd.Delivered && rd.Route != "caller" {
				rd.Garbled = true
			}
		}
	}
}

func appendUnique(list []string, item string) []string {
	for _, have := range list {
		if have == item {
			return list
		}
	}
	return append(list, item)
}

// ipscPath returns the minimal hop count from→to over the peer graph, or
// false if unlinked. from==to is 0 hops (local).
func ipscPath(adj map[string][]string, from, to string) (int, bool) {
	if from == to {
		return 0, true
	}
	seen := map[string]bool{from: true}
	next := []string{from}
	for hops := 1; len(next) > 0; hops++ {
		position := []string{}
		for _, site := range next {
			for _, peer := range adj[site] {
				if peer == to {
					return hops, true
				}
				if !seen[peer] {
					seen[peer] = true
					position = append(position, peer)
				}
			}
		}
		next = position
	}
	return 0, false
}

// simulateCall resolves and delivers one call, charging the ledger AFTER
// the deliverable set is computed — a call's own interval must not make it
// garble itself.
func simulateCall(st engineState, ledger *txLedger, c Call, id int) (CallResult, error) {
	radio, found := lookupRadio(st.network, c.From)
	if !found {
		return CallResult{}, fmt.Errorf("dmrsim: llamada %d: radio %q no existe en la topología", id, c.From)
	}
	_ = radio // resolved above for existence only; deliver() walks the list again

	tg, ok := st.tgByName[c.TG]
	if !ok {
		return CallResult{}, fmt.Errorf("dmrsim: llamada %d: talkgroup %q no existe", id, c.TG)
	}

	site := c.Site
	if site == "" {
		site = st.links.HomeOf(c.From)
		if site == "" {
			// The caller is unpositioned and forced no site: this is a
			// topology gap, not a physical outcome — say which, and keep
			// the whole scenario alive (the other calls still report).
			return CallResult{
				ID: id, From: c.From, TG: c.TG,
				AtMS: c.AtMS, EndMS: c.AtMS + c.DurationMS,
				Status: "no_uplink",
				Deliveries: []RadioDelivery{{
					Radio: c.From, Delivered: false, Reason: "no_position",
				}},
			}, nil
		}
	}
	rp := st.network.RepeaterByName(site)
	if rp == nil {
		return CallResult{}, fmt.Errorf("dmrsim: llamada %d: sitio %q no existe", id, site)
	}
	slot := dmr.TalkgroupSlot(tg, *rp)

	// A TG pinned to a slot the repeater does not carry simply cannot
	// key that site: Tier II slot hardware is per-carrier configuration,
	// so this is an operator-config outcome, reported honestly — not a
	// crash, and nothing airs (no ledger, no TG claim).
	if !rp.Slots.Enabled(slot) {
		return CallResult{
			ID: id, From: c.From, TG: c.TG, Site: site, Slot: slot,
			AtMS: c.AtMS, EndMS: c.AtMS + c.DurationMS,
			Status: "not_carried",
			Deliveries: []RadioDelivery{{
				Radio: c.From, Delivered: false, Reason: "slot_not_carried",
			}},
		}, nil
	}

	end := c.AtMS + c.DurationMS
	res := CallResult{
		ID:    id,
		From:  c.From,
		TG:    c.TG,
		Site:  site,
		Slot:  slot,
		AtMS:  c.AtMS,
		EndMS: end,
	}

	// ── the caller's own keyup: the repeater must actually hear it ────
	// Nothing airs if the call's uplink into its site never closes —
	// physically a dead key, not a channel dispute. Report it as the
	// call's own (failed) delivery and move on.
	if st.uplink[site][c.From] < 0 {
		res.Status = "no_uplink"
		res.Deliveries = []RadioDelivery{{
			Radio: c.From, Delivered: false, Reason: "no_margin",
		}}
		return res, nil
	}

	// ── slot contention at the keying site (before charging: the call's
	// own interval must not garble itself) ─────────────────────────────
	garble := ledger.overlapOn(site, slot, c.AtMS, end, st.network.Policy.GuardMS)
	if garble && st.network.Policy.Collision == dmr.CollisionDeny {
		res.Status = "denied"
		ledger.collided(site, slot, c.AtMS, end)
		res.Deliveries = []RadioDelivery{{
			Radio:     c.From,
			Delivered: false,
			Reason:    "denied_busy",
		}}
		return res, nil
	}

	// ── talkgroup claim (checked before claiming) ─────────────────────
	tgBusy := ledger.tgClaimed(c.TG, c.AtMS, end)
	res.Status = "aired"

	// Broadcast set: origin site, plus every site an IPSC path reaches
	// (when this call holds the TG claim).
	broadcast := map[string]int{site: 0}
	if !tgBusy {
		for i := range st.network.Repeaters {
			r := st.network.Repeaters[i]
			if r.Name == site {
				continue
			}
			if hops, ok := ipscPath(st.peersAdj, site, r.Name); ok {
				broadcast[r.Name] = hops
			}
		}
	}

	deliver(&res.Deliveries, st, ledger, c, tg, slot, end, garble, broadcast)

	// ── ledger, now: the deliverables above were computed against the
	// pre-call state ───────────────────────────────────────────────────
	ledger.charge(site, slot, c.AtMS, end)
	ledger.claimTG(c.TG, c.AtMS, end)
	if garble {
		ledger.collided(site, slot, c.AtMS, end)
	}

	return res, nil
}

// deliver fills one call's per-radio delivery table.
func deliver(out *[]RadioDelivery, st engineState, ledger *txLedger, c Call, tg dmr.Talkgroup, slot int, end int, garble bool, broadcast map[string]int) {
	for _, rd := range st.network.Radios {
		if rd.Name == c.From {
			*out = append(*out, RadioDelivery{
				Radio:     rd.Name,
				Delivered: true,
				Route:     "caller",
			})
			continue
		}

		home := st.links.HomeOf(rd.Name)
		if home == "" {
			// No uplink/downlink links at all: in the real pipeline that is
			// a radio without coordinates (ComputeLinks skips it). The
			// engine itself never needs coordinates — links are inputs.
			*out = append(*out, RadioDelivery{
				Radio:     rd.Name,
				Delivered: false,
				Reason:    "no_position",
			})
			continue
		}

		hops, carries := broadcast[home]
		if !carries {
			*out = append(*out, RadioDelivery{
				Radio:     rd.Name,
				Delivered: false,
				Reason:    "site_not_linked",
			})
			continue
		}

		margin := st.downlink[home][rd.Name]
		if margin < 0 {
			*out = append(*out, RadioDelivery{
				Radio:     rd.Name,
				Delivered: false,
				Reason:    "no_margin",
			})
			continue
		}

		if !subscribedTo(rd, tg) {
			*out = append(*out, RadioDelivery{
				Radio:     rd.Name,
				Delivered: false,
				Reason:    "tg_no_sub",
			})
			continue
		}

		// The radio hears garbled audio when ITS OWN site's slot carried a
		// competing PTT during the call (before this call charged).
		beenGarbled := garble || ledger.overlapOn(home, slot, c.AtMS, end, st.network.Policy.GuardMS)

		atMS := hops * st.network.Policy.IPSCLatencyMs
		route := "local"
		if hops > 0 {
			route = "ipsc"
		}
		*out = append(*out, RadioDelivery{
			Radio:     rd.Name,
			Delivered: true,
			AtMS:      atMS,
			Route:     route,
			Hops:      hops,
			Garbled:   beenGarbled,
		})
	}
}

func lookupRadio(n dmr.Network, name string) (dmr.Radio, bool) {
	for _, rd := range n.Radios {
		if rd.Name == name {
			return rd, true
		}
	}
	return dmr.Radio{}, false
}

// subscribedTo reports whether the radio carries the talkgroup. An empty
// list carries everything (service radio behaviour).
func subscribedTo(rd dmr.Radio, tg dmr.Talkgroup) bool {
	if len(rd.Talkgroups) == 0 {
		return true
	}
	for _, want := range rd.Talkgroups {
		if want == tg.Name || want == tg.ID {
			return true
		}
	}
	return false
}

// scenarioWindow is the run's whole time horizon: last call end plus one
// second of tail.
func scenarioWindow(s Scenario) int {
	last := 0
	for _, c := range s.Calls {
		if e := c.AtMS + c.DurationMS; e > last {
			last = e
		}
	}
	return last + 1000
}

type siteSlot struct {
	site string
	slot int
}

type interval struct{ a, b int }

func (iv interval) overlaps(a, b int) bool { return iv.a < b && a < iv.b }

// txLedger keeps every (site,slot) interval, every TG's claimed audio, and
// contention counts, so every decision is interval arithmetic on
// wire-past state.
type txLedger struct {
	siteTx  map[siteSlot][]interval
	tgTx    map[string][]interval
	collide map[siteSlot]int
}

func newTxLedger() *txLedger {
	return &txLedger{
		siteTx:  map[siteSlot][]interval{},
		tgTx:    map[string][]interval{},
		collide: map[siteSlot]int{},
	}
}

func (t *txLedger) charge(site string, slot, a, b int) {
	t.siteTx[siteSlot{site, slot}] = append(t.siteTx[siteSlot{site, slot}], interval{a, b})
}

func (t *txLedger) tgClaimed(tg string, a, b int) bool {
	for _, iv := range t.tgTx[tg] {
		if iv.overlaps(a, b) {
			return true
		}
	}
	return false
}

func (t *txLedger) claimTG(tg string, a, b int) {
	t.tgTx[tg] = append(t.tgTx[tg], interval{a, b})
}

func (t *txLedger) collided(site string, slot, a, b int) {
	t.collide[siteSlot{site, slot}]++
}

// overlapOn reports whether (site, slot) already carries audio that would
// overlap [a, b] minus the guard window at each end.
func (t *txLedger) overlapOn(site string, slot, a, b, guard int) bool {
	if guard > 0 {
		a += guard
		b -= guard
		if b <= a {
			return false
		}
	}
	for _, iv := range t.siteTx[siteSlot{site, slot}] {
		if iv.overlaps(a, b) {
			return true
		}
	}
	return false
}

func (t *txLedger) loads(window int) []SiteLoad {
	out := []SiteLoad{}
	for k, ivs := range t.siteTx {
		out = append(out, SiteLoad{
			Repeater:   k.site,
			Slot:       k.slot,
			TransmitMS: unionMS(ivs),
			DutyCycle:  float64(unionMS(ivs)) / float64(window),
			Collisions: t.collide[k],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repeater != out[j].Repeater {
			return out[i].Repeater < out[j].Repeater
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}

func unionMS(ivs []interval) int {
	if len(ivs) == 0 {
		return 0
	}
	sorted := append([]interval(nil), ivs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].a != sorted[j].a {
			return sorted[i].a < sorted[j].a
		}
		return sorted[i].b < sorted[j].b
	})
	total := 0
	cur := sorted[0]
	for _, iv := range sorted[1:] {
		if iv.a > cur.b {
			total += cur.b - cur.a
			cur = iv
			continue
		}
		if iv.b > cur.b {
			cur = interval{cur.a, iv.b}
		}
	}
	return total + cur.b - cur.a
}
