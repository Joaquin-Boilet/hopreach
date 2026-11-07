# Changelog

Formato: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) — este fork
registra sus cambios sobre [HopReach](https://github.com/A13xB0/hopreach)
upstream; las entradas siguientes cubren solo lo añadido/alterado por este
fork, no el historial upstream.

## [Unreleased]

## [0.1.0-dmr] — 2026-11-07 (fork: modo DMR Motorola TRBO)

### Added — dominio DMR

- `internal/dmr`: modelo de dominio de una red **Motorola TRBO DMR Tier II
  convencional** — repetidoras (sitio, mástil, par dúplex, slots TS1/TS2,
  peers IPSC), radios (handy/móvil) con presets por equipo, talkgroups
  clavados a slot, política de simulación (`overlap`/`deny`, guard, latencia
  IPSC) y parsing YAML con defaults y errores legibles.
- Presets de equipos Motorola: SLR5500, SLR1000, XPR8300e (repetidoras);
  DP4801e, DP4401e, SL7550e, R7a, DM4601e (radios). 2–3 dB conservadores
  por diseño; cualquier campo sobrescribe el preset.
- `internal/dmr/links.go`: cálculo de enlaces uplink/downlink por par
  (radio ↔ repetidora) contra terreno real vía `internal/propagation` —
  FSPL + difracción ITU-R P.526 sobre el DEM (sin reescribir la física
  del upstream: reuso paramétrico).
- `internal/dmrsim`: motor de simulación de llamadas por aritmética de
  intervalos (determinista): colisiones por (sitio, slot), guard de 60 ms
  (solape corto se absorbe, largo garbiliza ambas llamadas), claim de
  talkgroup por la red IPSC (evita doble entrega), latencia 80 ms/salto,
  duty cycle por ventana, ledger de espacio aéreo con política deny.
- `internal/dmrapi`: API HTTP DMR — `GET /api/dmr/topology` (red entera con
  cada enlace y su margen), `POST /api/dmr/simulate` (escenario JSON →
  Report), `GET /api/dmr/policy`.
- `cmd/dmrsim`: CLI headless (`-scenario`, `-json`, `-md`) + `-serve :addr`
  (sirve `public/` + la misma API que el deployment).
- `internal/meshsource`: backend `dmrtopology` para la interface `Source`
  existente — repetidoras `role="repeater"`, radios `role="companion"`,
  sin paquetes observados (calibración se salta sola).
- `internal/config`: `source.type: dmrtopology` + bloque `dmr.network_path`
  (default `dmr-network.yaml`).
- `cmd/hopreach-shareapi`: monta `dmrapi` automáticamente cuando config elige
  el backend DMR; un YAML roto degrada a plan sharing con un log — nunca un
  500 para todo el sitio.
- `cmd/hopreach/run.go`: altura por sitio para el raster de cobertura desde
  `dmr-network.yaml` (cada repetidora lleva su propia `antenna_height_m`).
- Frontend: `public/dmr.html` + `public/dmr.js` +
  `public/style-dmr.css` — página DMR con Leaflet: repetidoras y handys,
  enlaces coloreados por clase de margen (solid/ok/marginal/blocked),
  panel de escenarios con animación de llamadas y barras de slot.
- `examples/dmr/`: set de ejemplo **cordillera de Santiago, Chile** —
  `dmr-network.example.yaml` (Manquehue/Farellones/San Ramón, TGs FAENA1/
  MANTENCION/EMERGENCIA), `scenario.example.yaml`,
  `config.example-dmr.yaml`, `boundary-cordillera-rm.geojson`.

### Portabilidad

- `internal/demgrid`: mmap separado por plataforma — `mmap_unix.go`
  (mmap de kernel) y `mmap_windows.go` (heap plano, release no-op) para
  Windows como development host.
- `cmd/hopreach`: lock por plataforma — flock (unix) vs
  `LockFileEx` vía `syscall.NewLazyDLL` (Windows, sin x/sys).

### Docs

- `docs/DMR_MODE.md` (español): referencia del modo DMR — modelo/no-modelo,
  YAML de topología y escenario, CLI/API, física, checklist de adaptación.
- README + docs/TECHNICAL.md actualizados (fork note, sección DMR,
  project layout).
- `CHANGELOG.md` (este archivo).
- `examples/dmr/` documentado en DMR_MODE.md.

### Added — asistente agéntico (RAG + screenshots + UX)

- `tools/assistant/`: servicio de asistencia en línea **compatible con el
  formato de API de OpenAI** (`POST /v1/chat/completions`, `/v1/models`,
  streaming SSE) — cualquier cliente OpenAI-shape lo usa sin cambios.
- RAG sobre los MD del propio fork (README, CHANGELOG, DMR_MODE,
  TECHNICAL, ejemplos): chunker por encabezados, embeddings vía endpoint
  `/v1/embeddings` (bge-m3 en Ollama, funciona igual con vLLM/llama.cpp/
  comercial), store vectorial JSON+cosine inspeccionable
  (`data/docs-index.json`, reindex: `node tools/assistant/index.js`).
- Agente con herramientas OpenAI function-calling: `search_docs` (RAG,
  cita archivo+encabezado), **`take_screenshot`** (Playwright Chromium
  headless: captura real de la página del despliegue, con snapshot de
  texto y errores de consola — el agente ve lo que ve el usuario),
  `get_dmr_topology` y `run_dmr_simulate` (consulta y corre la API viva
  del producto: los márgenes que reporta el asistente son los mismos que
  calculó el DEM, nunca inventados).
- **UX como indicador principal de satisfacción**: cada turno se registra
  (latencia, herramientas, errores, fricción en las palabras del usuario)
  con score proxy 0–100 en `data/ux-events.jsonl`; la nota explícita del
  usuario (`POST /v1/ux/feedback`, 1–5) es ground truth y anula al proxy;
  readout agregado en `GET /v1/ux/metrics` (`satisfaction` +
  `satisfaction_source`).
- `tools/assistant/docs-shot.js`: valida el render de la página DMR
  (marcadores + reporte) y genera `docs/screenshot-dmr*.png` — test de
  UX disfrazado de producción de assets.
- Probado end-to-end: RAG citando docs, topología viva (SAN-RAMON↔
  HT-MAIPO −8,46/−13,10 dB blocked, 19,17 km del DEM real), lectura de
  captura de leyenda, e indicador UX con override de nota explícita.

### Nota de licencia

Upstream es **AGPL-3.0 + Commons Clause**: este fork hereda ambas. Los
archivos nuevos del fork suben bajo términos compatibles (AGPL-3.0).
