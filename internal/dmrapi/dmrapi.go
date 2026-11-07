// Package dmrapi serves the MotoTRBO adaptation's browser-facing API:
// the full DMR topology with every terrain-computed link margin, and the
// scenario runner the DMR page calls per click.
//
// It is deliberately separate from internal/meshapi: that wire shape is a
// CoreScope-compatibility contract whose users (the MeshCore map, the LoRa
// simulator) need MeshCore's vocabulary. The DMR vocabulary is different —
// it would be a modelling lie to try to express "which radios hear a
// talkgroup call" in a pubkey/relay/timeslot-for-LoRa envelope — so this
// handler speaks DMR directly, names fields for what they are, and skips
// the historical CoreScope-compat layer entirely.
//
// Mounting: cmd/hopreach-shareapi registers it when config.yaml selects
// source.type: dmrtopology (the always-on web process in a container);
// cmd/dmrsim -serve does the same for local development without Docker.
package dmrapi

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"hopreach/internal/demgrid"
	"hopreach/internal/dmr"
	"hopreach/internal/dmrsim"
	"hopreach/internal/propagation"
)

// TerrainSettings is where the DEM lives: same knobs the coverage pipeline
// uses (terrain.dem_zoom / dem_cache_dir / dem_tile_url_base in
// config.yaml), so one DMR deployment never quietly computes margins from
// a different elevation source than the map next to it.
type TerrainSettings struct {
	DEMZoom     int
	CacheDir    string
	TileURLBase string
}

// Handler serves the DMR API from a topology.
type Handler struct {
	network dmr.Network
	terrain TerrainSettings

	mu         sync.Mutex
	grid       *demgrid.Grid
	links      dmr.NetworkLinks
	fsplMode   bool
	computedAt time.Time
	computeErr error
	httpClient *http.Client
}

// New builds the handler. The topology is resolved eagerly — a broken
// dmr-network.yaml must fail at startup with a readable message, not at
// the first browser click.
func New(network dmr.Network, terrain TerrainSettings) (*Handler, error) {
	return &Handler{
		network:    network,
		terrain:    terrain,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Register mounts the three routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/dmr/topology", h.handleTopology)
	mux.HandleFunc("/api/dmr/simulate", h.handleSimulate)
	mux.HandleFunc("/api/dmr/policy", h.handlePolicy)
}

// ensureComputed computes (once) the terrain grid and every link margin.
// Subsequent calls reuse it: the DEM tiles are immutable and the margin
// math deterministic, so one pass per process is correct. Reload the
// server after editing dmr-network.yaml.
func (h *Handler) ensureComputed() (dmr.NetworkLinks, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.computeErr != nil {
		return dmr.NetworkLinks{}, h.fsplMode, h.computeErr
	}
	if h.grid != nil {
		return h.links, h.fsplMode, nil
	}

	links, fsplMode, err := h.compute()
	if err != nil {
		h.computeErr = err
		return dmr.NetworkLinks{}, true, err
	}
	h.links = links
	h.fsplMode = fsplMode
	h.computedAt = time.Now()
	return h.links, h.fsplMode, nil
}

// Padding + span caps for the DEM bbox. A typo'd coordinate in the YAML
// (a stray + in a southern latitude, a swapped zero) otherwise turns "the
// region" into "the Pacific Ocean" and the DEM download into an unbounded
// loop of ocean tiles — fail with a readable error instead.
const (
	dmrbboxPad       = 0.05
	dmrMaxSpanLatDeg = 3.0 // ≈ 330 km north–south
	dmrMaxSpanLonDeg = 4.0 // east–west (wider at these latitudes)
)

func (h *Handler) compute() (dmr.NetworkLinks, bool, error) {
	// The bbox is every positioned radio plus every repeater, with a pad
	// for bilinear neighbours and the propagation walk's endpoints.
	if has, bounds := h.bbox(); has {
		bounds.South -= dmrbboxPad
		bounds.North += dmrbboxPad
		bounds.West -= dmrbboxPad
		bounds.East += dmrbboxPad

		if span := bounds.North - bounds.South; span > dmrMaxSpanLatDeg {
			return dmr.NetworkLinks{}, false, fmt.Errorf(
				"dmr: la red cubre %.1f° de latitud (tope %.1f° ≈ 330 km): revisa coordenadas despistadas en dmr-network.yaml",
				span, dmrMaxSpanLatDeg)
		}
		if span := bounds.East - bounds.West; span > dmrMaxSpanLonDeg {
			return dmr.NetworkLinks{}, false, fmt.Errorf(
				"dmr: la red cubre %.1f° de longitud (tope %.1f°): revisa coordenadas despistadas en dmr-network.yaml",
				span, dmrMaxSpanLonDeg)
		}

		zoom := h.terrain.DEMZoom
		if zoom <= 0 {
			zoom = 11
		}
		cache := h.terrain.CacheDir
		if cache == "" {
			cache = "dem-cache"
		}
		base := h.terrain.TileURLBase
		if base == "" {
			base = "https://s3.amazonaws.com/elevation-tiles-prod/terrarium"
		}

		grid, err := demgrid.Load(demgrid.Bounds{
			South: bounds.South, North: bounds.North, West: bounds.West, East: bounds.East,
		}, zoom, cache, base, h.httpClient, nil)
		if err != nil {
			// Terrain failure must not silently become FSPL-only: say
			// loud what got skipped instead. (The caller may still ask
			// for FSPL-only explicitly; see below.)
			return dmr.NetworkLinks{}, false, fmt.Errorf("dmr: no se pudo cargar terreno (dem): %w", err)
		}
		links, err := dmr.ComputeLinks(h.network, grid)
		if err != nil {
			return dmr.NetworkLinks{}, false, err
		}
		return links, false, nil
	}

	// No positioned node at all: the FSPL mode is meaningless too, but the
	// topology is still servable (a roster with no coverage).
	n := h.network
	links, err := dmr.ComputeLinks(n, nil)
	if err != nil {
		return dmr.NetworkLinks{}, true, err
	}
	return links, true, nil
}

// bbox is the joined bounds of every positioned endpoint. Init to the
// signed-infinite sentinels: the earlier ±π sentinels were a southern-
// hemisphere landmine — Max(-π, -33.6) stays -π, so for any region south
// of the equator North/East never moved and the computed "bounds" spanned
// thousands of km of ocean tiles.
func (h *Handler) bbox() (bool, propagation.Bounds) {
	b := propagation.Bounds{
		South: math.Inf(1), North: math.Inf(-1),
		West: math.Inf(1), East: math.Inf(-1),
	}
	any := false
	add := func(lat, lon float64) {
		any = true
		b.South = math.Min(b.South, lat)
		b.North = math.Max(b.North, lat)
		b.West = math.Min(b.West, lon)
		b.East = math.Max(b.East, lon)
	}
	for i := range h.network.Repeaters {
		add(h.network.Repeaters[i].Lat, h.network.Repeaters[i].Lon)
	}
	for i := range h.network.Radios {
		rd := h.network.Radios[i]
		if rd.Lat != nil && rd.Lon != nil {
			add(*rd.Lat, *rd.Lon)
		}
	}
	return any, b
}

// Δ ── wire shapes ──────────────────────────────────────────────────────

type wireRepeater struct {
	Name      string   `json:"name"`
	DMRID     uint32   `json:"dmr_id,omitempty"`
	Model     string   `json:"model"`
	Lat       float64  `json:"lat"`
	Lon       float64  `json:"lon"`
	MastM     float64  `json:"mast_height_m"`
	PowerW    float64  `json:"tx_power_w"`
	GainDBi   float64  `json:"antenna_gain_dbi"`
	SensDBm   float64  `json:"rx_sensitivity_dbm"`
	TXMHz     float64  `json:"tx_freq_mhz"`
	RXMHz     float64  `json:"rx_freq_mhz"`
	Slots     []int    `json:"slots"`
	IPSCPeers []string `json:"peers,omitempty"`
	Scope     string   `json:"scope,omitempty"`
}

type wireRadio struct {
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Model       string   `json:"model"`
	Lat         *float64 `json:"lat,omitempty"`
	Lon         *float64 `json:"lon,omitempty"`
	HeightM     float64  `json:"height_m"`
	PowerW      float64  `json:"tx_power_w"`
	SensDBm     float64  `json:"rx_sensitivity_dbm"`
	Talkgroups  []string `json:"tgs,omitempty"`
	ServingSite string   `json:"home_site,omitempty"`
}

type wireLink struct {
	Repeater   string  `json:"repeater"`
	Radio      string  `json:"radio"`
	Class      string  `json:"class"`
	MarginDB   float64 `json:"margin_db"`
	PathDB     float64 `json:"path_db"`
	DistanceKm float64 `json:"distance_km"`
}

type wireTopology struct {
	GeneratedAt string            `json:"generated_at"`
	NetworkName string            `json:"network_name"`
	FSPLMode    bool              `json:"fspl_mode"`
	Policy      dmr.Policy        `json:"policy"`
	Talkgroups  []dmr.Talkgroup   `json:"talkgroups"`
	Repeaters   []wireRepeater    `json:"repeaters"`
	Radios      []wireRadio       `json:"radios"`
	Uplinks     []wireLink        `json:"uplinks"`
	Downlinks   []wireLink        `json:"downlinks"`
	ServingOf   map[string]string `json:"serving_of"`
}

func (h *Handler) topologyWire(links dmr.NetworkLinks, fsplMode bool) wireTopology {
	out := wireTopology{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		NetworkName: h.network.Name,
		FSPLMode:    fsplMode,
		Policy:      h.network.Policy,
		Talkgroups:  h.network.Talkgroups,
		ServingOf:   links.ServingOf,
	}

	out.Repeaters = make([]wireRepeater, 0, len(h.network.Repeaters))
	for i := range h.network.Repeaters {
		r := h.network.Repeaters[i]
		slots := []int{}
		if r.Slots.TS1 {
			slots = append(slots, 1)
		}
		if r.Slots.TS2 {
			slots = append(slots, 2)
		}
		out.Repeaters = append(out.Repeaters, wireRepeater{
			Name: r.Name, DMRID: r.DMRID, Model: r.Model,
			Lat: r.Lat, Lon: r.Lon,
			MastM: r.AntennaHeightM, PowerW: r.TxPowerW, GainDBi: r.AntennaGainDBi,
			SensDBm: r.RxSensitivityDBm,
			TXMHz:   r.TXMHz, RXMHz: r.RXMHz,
			Slots: slots, IPSCPeers: r.IPSCPeers, Scope: r.Scope,
		})
	}

	out.Radios = make([]wireRadio, 0, len(h.network.Radios))
	for i := range h.network.Radios {
		rd := h.network.Radios[i]
		wr := wireRadio{
			Name: rd.Name, Role: rd.Role, Model: rd.Model,
			Lat: rd.Lat, Lon: rd.Lon,
			HeightM: rd.AntennaHeightM, PowerW: rd.TxPowerW,
			SensDBm: rd.RxSensitivityDBm, Talkgroups: rd.Talkgroups,
		}
		wr.ServingSite = links.ServingOf[rd.Name]
		out.Radios = append(out.Radios, wr)
	}

	toWire := func(ls []dmr.Link) []wireLink {
		wl := make([]wireLink, 0, len(ls))
		for _, l := range ls {
			wl = append(wl, wireLink{
				Repeater: l.Repeater, Radio: l.Radio,
				Class: l.Class(), MarginDB: l.MarginDB,
				PathDB: l.PathDB, DistanceKm: l.DistanceKm,
			})
		}
		return wl
	}
	out.Uplinks = toWire(links.Uplinks)
	out.Downlinks = toWire(links.Downlinks)
	return out
}

// Δ handlers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func (h *Handler) handleTopology(w http.ResponseWriter, r *http.Request) {
	links, fsplMode, err := h.ensureComputed()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, h.topologyWire(links, fsplMode))
}

func (h *Handler) handlePolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"network_name": h.network.Name,
		"policy":       h.network.Policy,
	})
}

// handleSimulate takes a dmrsim.Scenario (JSON) and returns the Report.
func (h *Handler) handleSimulate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var sc dmrsim.Scenario
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "escenario JSON inválido: " + err.Error()})
		return
	}
	if err := sc.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	links, fsplMode, err := h.ensureComputed()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rep, err := dmrsim.Run(h.network, links, sc, !fsplMode)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// Links exposes the one-time-computed link set (and whether terrain was
// actually modelled) to non-HTTP callers — the CLI reuses this pass instead
// of duplicating the DEM/bbox plumbing.
func (h *Handler) Links() (dmr.NetworkLinks, bool, error) {
	return h.ensureComputed()
}

// Warmup forces the one-time terrain + link computation and logs a readiness
// line; servers call it at boot so the first browser click is not the one
// that pays for the DEM download.
func (h *Handler) Warmup() {
	start := time.Now()
	if _, _, err := h.ensureComputed(); err != nil {
		log.Printf("dmrapi: computo de enlaces falló en warmup: %v (la primera petición lo reintentará)", err)
		return
	}
	log.Printf("dmrapi: enlaces de la topología calculados en %s", time.Since(start).Truncate(time.Millisecond))
}
