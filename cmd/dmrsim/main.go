// Command dmrsim is the MotoTRBO adaptation's own entry point. It runs a
// DMR scenario (push-to-talk calls over a topology file) either headless —
// the report lands on stdout as text, markdown or JSON — or as a local web
// server that serves public/ plus internal/dmrapi's routes, the same wiring
// the always-on shareapi gets when source.type is dmrtopology.
//
// The deliberate split from cmd/hopreach: the original binary is a batch
// coverage pipeline that assumes observed packets and calibration. DMR
// modelling is a different vocabulary (repeaters, slots, talkgroups, IPSC)
// with no observations to replay, so it gets a small, honest CLI whose only
// inputs are YAML files it fully controls.
//
// Examples:
//
//	dmrsim -config config.yaml -network dmr-network.yaml -scenario escenario.yaml
//	dmrsim -network dmr-network.yaml -scenario escenario.yaml -json
//	dmrsim -network dmr-network.yaml -serve :8090
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	yconfig "hopreach/internal/config"
	"hopreach/internal/dmr"
	"hopreach/internal/dmrapi"
	"hopreach/internal/dmrsim"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

func main() {
	configFlag := flag.String("config", "", "path to config.yaml (default: $HOPREACH_CONFIG, then ./config.yaml; only used for terrain.dem_* settings)")
	networkFlag := flag.String("network", "", "path to the DMR topology YAML (default: dmr.network_path from config.yaml, then ./dmr-network.yaml)")
	scenarioFlag := flag.String("scenario", "", "path to the scenario YAML (required unless -serve)")
	jsonOut := flag.Bool("json", false, "print the report as JSON instead of text")
	mdOut := flag.Bool("md", false, "print the report as markdown instead of text")
	serveFlag := flag.String("serve", "", "serve public/ + the DMR API on this address (e.g. :8090) instead of running a scenario")
	flag.Parse()

	yc, _, err := yconfig.Load(*configFlag)
	if err != nil {
		fatal(err)
	}

	networkPath := *networkFlag
	if networkPath == "" {
		networkPath = yc.DMR.NetworkPath
		if networkPath == "" {
			networkPath = yconfig.Default().DMR.NetworkPath
		}
	}

	if *serveFlag != "" {
		serve(*serveFlag, yc, networkPath)
		return
	}

	n, err := dmr.LoadYAML(networkPath)
	if err != nil {
		fatal(err)
	}
	links, fspl, err := computeLinks(n, yc)
	if err != nil {
		fatal(err)
	}

	if *scenarioFlag == "" {
		fatal(fmt.Errorf("dmrsim: falta -scenario (o usa -serve para modo servidor)"))
	}
	body, err := os.ReadFile(*scenarioFlag)
	if err != nil {
		fatal(fmt.Errorf("dmrsim: leyendo escenario %s: %w", *scenarioFlag, err))
	}
	sc, err := dmrsim.ParseScenarioYAML(body)
	if err != nil {
		fatal(err)
	}

	rep, err := dmrsim.Run(n, links, sc, !fspl)
	if err != nil {
		fatal(err)
	}

	switch {
	case *jsonOut:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fatal(err)
		}
	case *mdOut:
		fmt.Print(reportMarkdown(rep))
	default:
		fmt.Print(reportText(rep))
	}
	if rep.FSPLMode {
		fmt.Fprintln(os.Stderr, "dmrsim: ATENCIÓN: sin terreno (FSPL puro); márgenes optimistas")
	}
}

// computeLinks resolves the topology's one-time terrain pass — margins
// need a DEM just like the coverage pipeline does. Reusing dmrapi.Handler
// keeps one implementation of the bbox/DEM/cache logic.
func computeLinks(n dmr.Network, yc yconfig.Config) (dmr.NetworkLinks, bool, error) {
	h, err := dmrapi.New(n, terrainSettings(yc))
	if err != nil {
		return dmr.NetworkLinks{}, true, err
	}
	return h.Links()
}

// serve is dmrsim as a development web server: the exact same handler set
// the container exposes (api + static site), minus plan sharing — that one
// stays in shareapi because it stores state, and this one is meant to boot
// against an edited YAML in ten seconds.
func serve(addr string, yc yconfig.Config, networkPath string) {
	n, err := dmr.LoadYAML(networkPath)
	if err != nil {
		fatal(err)
	}
	h, err := dmrapi.New(n, terrainSettings(yc))
	if err != nil {
		fatal(err)
	}

	mux := http.NewServeMux()
	h.Register(mux)

	switch {
	case dirExists("public"):
		fs := http.FileServer(http.Dir("public"))
		mux.Handle("/", fs)
		log.Printf("dmrsim: sirviendo public/ en http://localhost%s/dmr.html", addr)
	default:
		// No public/ next to the binary (e.g. rune from a built artifact):
		// the API still works, which is what tests and scripts want.
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "dmrsim: no hay directorio public/ junto al ejecutable; sirve la API con /api/dmr/*", http.StatusNotFound)
		})
		log.Printf("dmrsim: sin public/ a mano; API en http://localhost%s/api/dmr/topology", addr)
	}

	log.Printf("dmrsim: red %q desde %s — escuchando en %s", n.Name, networkPath, addr)
	go h.Warmup()
	log.Fatal(http.ListenAndServe(addr, mux))
}

func terrainSettings(yc yconfig.Config) dmrapi.TerrainSettings {
	return dmrapi.TerrainSettings{
		DEMZoom:     yc.Terrain.DEMZoom,
		CacheDir:    yc.Terrain.DEMCacheDir,
		TileURLBase: yc.Terrain.DEMTileURLBase,
	}
}

func dirExists(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "dmrsim: %v\n", err)
	os.Exit(1)
}

// ── report renderers ────────────────────────────────────────────────────
// The CLI's whole output layer: text for the operator's terminal, markdown
// for pasting into an issue or the vault, JSON for anything that wants to
// script around the engine. All three read only the Report — rendering
// never re-derives outcomes the engine already settled.

func deliveryLine(d dmrsim.RadioDelivery) string {
	switch {
	case d.Delivered && d.Route == "caller":
		return "origen de la llamada"
	case d.Delivered && d.Route == "local":
		return "recibido por su sitio"
	case d.Delivered && d.Route == "ipsc":
		return fmt.Sprintf("recibido vía IPSC (%d salto/s)", d.Hops)
	case d.Reason == "no_position":
		return "sin posición → sin enlaces"
	case d.Reason == "site_not_linked":
		return "su sitio no llega a ninguna llamada"
	case d.Reason == "no_margin":
		return "sin margen para su sitio"
	case d.Reason == "tg_no_sub":
		return "no suscrito al TG"
	case d.Reason == "denied_busy":
		return "llamada denegada (canal ocupado)"
	default:
		return d.Reason
	}
}

// labelStatus names a CallResult status the way an operator says it.
func labelStatus(s string) string {
	switch s {
	case "aired":
		return "AL AIRE"
	case "denied":
		return "DENEGADA (canal ocupado)"
	case "no_uplink":
		return "SIN UPLINK (dead key: no la escucha su repetidora)"
	case "not_carried":
		return "SLOT NO LLEVADO (TG en TS no habilitada en el sitio)"
	default:
		return strings.ToUpper(s)
	}
}

func reportText(rep dmrsim.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Red: %s — ventana %d ms", rep.NetworkName, rep.WindowMS)
	if rep.FSPLMode {
		b.WriteString(" [FSPL sin terreno]")
	}
	b.WriteString("\n\n")

	for _, c := range rep.Calls {
		status := labelStatus(c.Status)
		fmt.Fprintf(&b, "Llamada #%d %s → TG %s @ %s TS%d (%d–%d ms) %s",
			c.ID, c.From, c.TG, c.Site, c.Slot, c.AtMS, c.EndMS, status)
		if c.Garbled {
			b.WriteString(" [CON SOLAPE]")
		}
		b.WriteString("\n")
		for _, d := range c.Deliveries {
			fmt.Fprintf(&b, "  - %-16s %s", d.Radio, deliveryLine(d))
			if d.Garbled {
				b.WriteString(" [parcialmente corrompido]")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(rep.Loads) > 0 {
		b.WriteString("Carga por (site, slot) durante la ventana:\n")
		for _, l := range sortedCopy(rep.Loads) {
			fmt.Fprintf(&b, "  - %-16s TS%d: %6.1f%% aire (%d ms", l.Repeater, l.Slot, l.DutyCycle*100, l.TransmitMS)
			if l.Collisions > 0 {
				fmt.Fprintf(&b, ", %d colisiones", l.Collisions)
			}
			b.WriteString(")\n")
		}
	}
	for _, n := range rep.Notes {
		b.WriteString("Nota: " + n + "\n")
	}
	return b.String()
}

func reportMarkdown(rep dmrsim.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Simulación DMR — %s\n\n", rep.NetworkName)
	if rep.FSPLMode {
		b.WriteString("> ⚠️ Sin modelo de terreno: márgenes en espacio libre (optimistas).\n\n")
	}
	fmt.Fprintf(&b, "Ventana escenarios: **%d ms**.\n\n", rep.WindowMS)

	for _, c := range rep.Calls {
		fmt.Fprintf(&b, "## Llamada #%d: %s → %s\n\n", c.ID, c.From, c.TG)
		fmt.Fprintf(&b, "Site %s, TS%d, %d–%d ms. Estado: **%s**%s.\n\n",
			c.Site, c.Slot, c.AtMS, c.EndMS, labelStatus(c.Status),
			pick(c.Garbled, ", con solape garbilizado", ""))
		b.WriteString("| Radio | Resultado |\n|---|---|\n")
		for _, d := range c.Deliveries {
			fmt.Fprintf(&b, "| %s | %s%s |\n", d.Radio, deliveryLine(d), pick(d.Garbled, " (solape)", ""))
		}
		b.WriteString("\n")
	}

	if len(rep.Loads) > 0 {
		b.WriteString("## Carga de canal\n\n| Site | TS | Aire (ms) | Duty cycle | Colisiones |\n|---|---|---|---|---|\n")
		for _, l := range sortedCopy(rep.Loads) {
			fmt.Fprintf(&b, "| %s | %d | %d | %.1f%% | %d |\n",
				l.Repeater, l.Slot, l.TransmitMS, l.DutyCycle*100, l.Collisions)
		}
		b.WriteString("\n")
	}

	for _, note := range rep.Notes {
		fmt.Fprintf(&b, "Nota: %s\n", note)
	}
	return b.String()
}

func pick(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

// sortedCopy keeps deterministic output for the engine's SiteLoad list.
func sortedCopy(items []dmrsim.SiteLoad) []dmrsim.SiteLoad {
	out := append([]dmrsim.SiteLoad{}, items...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repeater != out[j].Repeater {
			return out[i].Repeater < out[j].Repeater
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}
