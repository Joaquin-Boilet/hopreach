package dmrsim

// A Scenario is the list of calls the request wants simulated over the
// network's topology: "if radio UNID-1 keys TG FAENA1 for 8 s at t=5 s,
// who hears it, what does the network have to fight for, and where does
// it knot?".
//
// Times are in milliseconds from scenario t=0. All intervals are treated
// with plain interval arithmetic — there is no clock-stepping loop, so a
// run is deterministic and cheap enough for the browser to request per
// click.
import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Call is one push-to-talk: a radio keys a talkgroup.
type Call struct {
	// From is the calling radio's name. Required.
	From string `json:"from" yaml:"from"`

	// TG is the talkgroup name as declared in the network's talkgroups.
	TG string `json:"tg" yaml:"tg"`

	// AtMS is when the PTT is keyed (ms from t=0).
	AtMS int `json:"at_ms" yaml:"at_ms"`

	// DurationMS is how long the caller holds the PTT.
	DurationMS int `json:"duration_ms" yaml:"duration_ms"`

	// Site forces the calling radio's home repeater instead of the
	// strongest-uplink default. Optional: an operator's override for
	// whoever tests a specific site.
	Site string `json:"site,omitempty" yaml:"site,omitempty"`
}

// Scenario is the whole input.
type Scenario struct {
	Calls []Call `json:"calls" yaml:"calls"`
}

// ParseScenarioYAML parses the CLI-shaped scenario.
func ParseScenarioYAML(body []byte) (Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(body, &s); err != nil {
		return s, fmt.Errorf("dmrsim: parseando escenario YAML: %w", err)
	}
	return s, s.Validate()
}

// Validate checks the scenario's own invariants. Reference resolution
// against the network (unknown radios, unknown talkgroups) happens in Run,
// where the full context exists.
func (s Scenario) Validate() error {
	for i, c := range s.Calls {
		if c.From == "" {
			return fmt.Errorf("dmrsim: llamada %d sin radio de origen", i+1)
		}
		if c.TG == "" {
			return fmt.Errorf("dmrsim: llamada de %q sin talkgroup", c.From)
		}
		if c.DurationMS <= 0 {
			return fmt.Errorf("dmrsim: llamada de %q con duración no positiva", c.From)
		}
		if c.AtMS < 0 {
			return fmt.Errorf("dmrsim: llamada de %q con inicio negativo", c.From)
		}
	}
	return nil
}
