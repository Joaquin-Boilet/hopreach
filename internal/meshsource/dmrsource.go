// A DMR topology backend: HopReach's third data source, and the one that
// watches nobody.
//
// The two observation backends (CoreScope, Beacon) read other people's
// record of MeshCore traffic. The dmrtopology backend instead models a
// MotoTRBO network directly from its own topology file (dmr-network.yaml):
// repeaters with their site heights and frequency pairs, handys with their
// positions, talkgroups and IPSC peer links. There are no observed packets
// — the file IS the data — so this source is deliberately honest about
// that: packets come back empty, scopes come from the file's own region
// tags, and reach links are absent (calibration therefore skips itself the
// same way it does for any backend with no observations).
//
// The mapping into MeshCore's node vocabulary is kept deliberately
// narrow: repeaters become role="repeater" nodes (so the coverage
// pipeline, the map markers and the region filter work unchanged) and
// radios become role="companion" nodes only in FetchAllNodes, matching
// the DMR page's sidebar roster.
package meshsource

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"hopreach/internal/dmr"
)

// DMRTopologySource serves a dmr.Network through the Source interface.
type DMRTopologySource struct {
	mu      sync.Mutex
	path    string
	network dmr.Network
	loaded  bool
}

// NewDMRTopologySource builds the source for one topology file path.
func NewDMRTopologySource(path string) *DMRTopologySource {
	return &DMRTopologySource{path: path}
}

// ensure resolves (and caches) the network once per process — a caller
// edited the file mid-run and a reparse is what changed output, so a
// deliberate lock-free expiry is deliberately not provided.
func (s *DMRTopologySource) ensure() (dmr.Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return s.network, nil
	}
	body, err := os.ReadFile(s.path)
	if err != nil {
		return dmr.Network{}, fmt.Errorf("dmrtopology: leyendo %s: %w", s.path, err)
	}
	n, err := dmr.ParseYAML(body)
	if err != nil {
		return dmr.Network{}, fmt.Errorf("dmrtopology: %s: %w", s.path, err)
	}
	s.network = n
	s.loaded = true
	return n, nil
}

// Reload drops the cached parse (tests and the share-api's recompute
// trigger).
func (s *DMRTopologySource) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = false
	s.network = dmr.Network{}
}

func (s *DMRTopologySource) Name() string {
	if n, err := s.ensure(); err == nil && n.Name != "" {
		return "dmrtopology (" + n.Name + ")"
	}
	return "dmrtopology"
}

// Capabilities: the topology file knows every scope declared on it — the
// completeness an observation backend usually cannot promise.
func (s *DMRTopologySource) Capabilities() Capabilities {
	return Capabilities{ScopeCatalog: true}
}

// FetchRepeaters returns every repeater as a MeshCore-shaped node. The
// public key slot carries a stable synthetic id ("dmr:"+name) — the
// pipeline uses it as a grouping key only, and no MeshCore decode exists
// here to collide with.
func (s *DMRTopologySource) FetchRepeaters(ctx context.Context) ([]Node, error) {
	n, err := s.ensure()
	if err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(n.Repeaters))
	for i := range n.Repeaters {
		r := &n.Repeaters[i]
		lat, lon := r.Lat, r.Lon
		scope := r.Scope
		h := r.AntennaHeightM
		out = append(out, Node{
			PublicKey:    dmrNodeKey(r.Name),
			Name:         r.Name,
			Role:         "repeater",
			Lat:          &lat,
			Lon:          &lon,
			LastHeard:    time.Now(),
			AdvertCount:  0,
			RelayCount1h: 0,
			// The repeater's scope tag rides default_scope so the map's
			// region filter treats it like every other backend's tag.
			DefaultScope: scope,
			// Lift mast height into the node so the pipeline's per-site
			// raster height can honour dmr-network.yaml instead of one
			// config-wide number.
			nodeHeight: &h,
		})
	}
	return out, nil
}

// FetchAllNodes returns repeaters plus every positioned radio as a
// "companion" node, so any MeshCore-shaped consumer that wants the full
// roster can have it. Unpositioned radios are included too, positionless
// — the roster is the truth, not a map.
func (s *DMRTopologySource) FetchAllNodes(ctx context.Context) ([]Node, error) {
	n, err := s.ensure()
	if err != nil {
		return nil, err
	}
	reps, err := s.FetchRepeaters(ctx)
	if err != nil {
		return nil, err
	}
	out := append([]Node{}, reps...)
	for i := range n.Radios {
		rd := n.Radios[i]
		node := Node{
			PublicKey: dmrNodeKey(rd.Name),
			Name:      rd.Name,
			Role:      "companion",
		}
		if rd.Lat != nil && rd.Lon != nil {
			lat, lon := *rd.Lat, *rd.Lon
			node.Lat = &lat
			node.Lon = &lon
		}
		out = append(out, node)
	}
	return out, nil
}

// FetchReach: nothing observed. The calibration pass reads this as "no
// observations" and skips itself — the DMR mode's truthfulness over any
// backend that invents data.
func (s *DMRTopologySource) FetchReach(ctx context.Context, pubkey string, days int) ([]ReachLink, error) {
	return nil, nil
}

// FetchScopes lists the distinct region tags in the topology file.
func (s *DMRTopologySource) FetchScopes(ctx context.Context) ([]string, error) {
	n, err := s.ensure()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for i := range n.Repeaters {
		scope := strings.TrimSpace(n.Repeaters[i].Scope)
		if scope != "" && !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	return out, nil
}

// FetchPacketsBetween: no packets exist to observe.
func (s *DMRTopologySource) FetchPacketsBetween(ctx context.Context, from, to time.Time, limit int) ([]Packet, error) {
	return []Packet{}, nil
}

// FetchPacketDetail: nothing to detail.
func (s *DMRTopologySource) FetchPacketDetail(ctx context.Context, hash string) (Packet, error) {
	return Packet{}, fmt.Errorf("dmrtopology: la observación de paquetes no existe en este backend")
}

// FetchRegionParticipation: the scopes' truth is the file itself, so
// participation is reported directly from the declared membership rather
// than observed relay events — scoped counts 1 per (node, region).
func (s *DMRTopologySource) FetchRegionParticipation(ctx context.Context, since time.Time, regionNames []string) (Participation, error) {
	n, err := s.ensure()
	if err != nil {
		return Participation{}, err
	}
	part := Participation{Scoped: map[string]map[string]int{}, Unscoped: map[string]int{}}
	names := regionNames
	if len(names) == 0 {
		names, _ = s.FetchScopes(ctx)
	}
	wanted := map[string]bool{}
	for _, rn := range names {
		wanted[rn] = true
	}
	for i := range n.Repeaters {
		r := n.Repeaters[i]
		scope := strings.TrimSpace(r.Scope)
		if scope == "" || !wanted[scope] {
			continue
		}
		part.Scoped[dmrNodeKey(r.Name)] = map[string]int{scope: 1}
	}
	return part, nil
}

// dmrNodeKey keeps the synthetic id shape in one place.
func dmrNodeKey(name string) string {
	return "dmr:" + strings.ToLower(name)
}

// NodeHeight returns a node's own mast height when the source supplied
// one (dmrtopology always does for repeaters; observation backends never
// do). The pipeline honours this over one config-wide mast height.
func (n Node) NodeHeight() (float64, bool) {
	if n.nodeHeight != nil {
		return *n.nodeHeight, true
	}
	return 0, false
}
