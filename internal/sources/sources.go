// Package sources builds the configured observation backend.
//
// It lives apart from meshsource because the adapters depend on that package
// for the canonical types — a factory inside it would be an import cycle.
package sources

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"hopreach/internal/beacon"
	"hopreach/internal/config"
	"hopreach/internal/corescope"
	"hopreach/internal/meshsource"
)

// FromConfig builds the configured observation backend.
//
// CoreScope remains the default; a Beacon block with `enabled: true` replaces
// it. Selection is deliberately explicit rather than "whichever URL is set",
// so a half-filled config can't silently swap the data underneath a map.
func FromConfig(cfg config.Config) (meshsource.Source, error) {
	src, err := backendFor(cfg)
	if err != nil {
		return nil, err
	}
	if names := nonEmpty(cfg.Source.Scopes); len(names) > 0 {
		return withConfiguredScopes{Source: src, names: names}, nil
	}
	return src, nil
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func backendFor(cfg config.Config) (meshsource.Source, error) {
	kind, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	switch kind {
	case config.SourceBeacon:
		timeout := time.Duration(cfg.Beacon.RequestTimeoutSeconds * float64(time.Second))
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		c, err := beacon.New(
			cfg.Beacon.APIURL,
			cfg.Beacon.IATAs,
			&http.Client{Timeout: timeout},
		)
		if err != nil {
			// Refuse to start rather than fall back to CoreScope: silently
			// serving a different network's data is worse than not starting.
			return nil, fmt.Errorf("beacon data source: %w", err)
		}
		c.DetailConcurrency = cfg.Beacon.DetailConcurrency
		return c, nil

	case config.SourceCoreScope:
		timeout := time.Duration(cfg.CoreScope.RequestTimeoutSeconds * float64(time.Second))
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		if cfg.CoreScope.APIURL == "" {
			return nil, fmt.Errorf("corescope data source: corescope.api_url is required")
		}
		return meshsource.NewCoreScopeSource(
			corescope.NewClient(cfg.CoreScope.APIURL, &http.Client{Timeout: timeout}),
		), nil

	case config.SourceDMRTopology:
		// The MotoTRBO adaptation: the network file is the backend. Its
		// path is a required, explicit value — defaulting to an absent
		// file would silently map an empty region instead of failing a
		// half-configured DMR deployment.
		if strings.TrimSpace(cfg.DMR.NetworkPath) == "" {
			return nil, fmt.Errorf("dmrtopology data source: dmr.network_path is required")
		}
		return meshsource.NewDMRTopologySource(cfg.DMR.NetworkPath), nil

	default:
		return nil, fmt.Errorf("unknown source type %q", kind)
	}
}

// Build the completeness assertions the old switch had implicitly.
var _ meshsource.Source = (*meshsource.CoreScopeSource)(nil)
var _ meshsource.Source = (*meshsource.DMRTopologySource)(nil)
var _ meshsource.Source = (*beacon.Client)(nil)
