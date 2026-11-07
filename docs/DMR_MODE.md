# Modo DMR: simulación MotoTRBO (Tier II convencional + IPSC)

Este fork adapta la física RF de HopReach (DEM real + presupuesto de
enlaces + difracción) a la **simulación de una red Motorola TRBO en DMR**:
repetidoras convencionales Tier II con 2 timeslots TDMA, pares de
frecuencias dúplex, handys Motorola y enlaces IP Site Connect (IPSC)
entre sitios.

> La regla del proyecto: **la física no se inventa**. Cada margen en dB
> que ves sale del modelo de propagación contra terreno real (tiles DEM
> terrarium), el mismo motor que usa el mapa de cobertura original. Lo
> que cambia es el vocabulario: ya no son "relays MeshCore", son
> repetidoras, slots, talkgroups y enlaces IPSC.

---

## 1. Qué modela y qué no

**Modela:**

- Uplink y downlink de cada par (radio ↔ repetidora) con margen en dB
  contra terreno: FSPL + difracción ITU-R P.526 sobre el DEM, con presets
  de equipos Motorola (SLR5500, SLR1000, XPR8300e; DP4801e, DP4401e,
  SL7550e, R7a, DM4601e) y altura de mástil por sitio.
- TDMA Tier II: un canal = 2 slots (TS1/TS2); cada (sitio, slot) lleva
  una sola llamada a la vez.
- **Guard de 60 ms**: un solape más corto que el guard se absorbe (el
  motor trata el choque como sub-trama y sigue); un solape más largo
  corrompe **ambas** llamadas (`garbled`) retroactivamente.
- **Talkgroups**: se simula el claim del TG por la red IPSC. Una llamada
  no puede pisar el mismo TG en otra repetidora conectada dentro de su
  ventana: el claim hace que la otra repetidora no re-anuncie (evita la
  "doble llamada") y el reporte muestra el claim.
- **Latencia IPSC**: 80 ms por salto por defecto. Un radio que escucha el
  TG vía un sitio vecino recibe el audio con ese retardo acumulado por la
  cadena de enlaces.
- **Duty cycle** por (sitio, slot) durante la ventana del escenario, con
  contador de colisiones.
- **Política de colisión configurable (`overlap` | `deny`)**: con
  `overlap`, la segunda PTT garbiliza ambas llamadas desde el primer ms
  por sobre el guard; con `deny`, la segunda PTT se rechaza
  (`denied_busy`, "canal ocupado") y no carga el ledger de TX.

**No modela (honestidad deliberada):**

- Encriptación ni señalización avanzada (RAS, Privacy Plus, ARC4/256): lo
  que decide la entrega es física, no seguridad.
- Radios en modo directo (sin repetidora) ni roaming de slots entre
  sitios: cada radio pertenece a un sitio (el de su uplink más fuerte,
  o el que el escenario forza).
- Estaciones de mando por consola/Dispatcher remoto vía IPSC multi-sitio
  (solo se simula el flujo de audio de las llamadas declaradas).
- Calvo de RSSI "en vivo": el margen es por presupuesto estático de la
  llamada; no simula desvanecimiento dinámico por moverse mientras habla.
- Paquetes observados de observabilidad: este backend no mira una red
  real en producción; el archivo de topología **es** la verdad y no hay
  calibración contra tráfico observado.

---

## 2. Los archivos

| Archivo | Qué es |
|---|---|
| `dmr-network.yaml` | La **topología**: repetidoras (sitio, mástil, par de frecuencias, peers IPSC, slots), handys/móviles con posición, talkgroups y política. Ejemplo: `examples/dmr/dmr-network.example.yaml`. |
| escenario YAML | Las **llamadas** a simular: quién pide qué TG, cuándo y cuánto dura. Ejemplo: `examples/dmr/scenario.example.yaml`. |
| `config.yaml` | El mismo config del repo; el modo DMR lee `source.type: dmrtopology`, `dmr.network_path` y `terrain.dem_zoom` / `terrain.dem_cache_dir` / `terrain.dem_tile_url_base`. Ejemplo: `examples/dmr/config.example-dmr.yaml`. |
| límite GeoJSON | Opcional: recorta el área de tu red. Ejemplo: `examples/dmr/boundary-cordillera-rm.geojson`. |

### Referencia de `dmr-network.yaml`

```yaml
network_name: "Cordillera Santiago — ejemplo"
sim_policy:
  collision: overlap      # overlap | deny
  ipsc_latency_ms: 80     # ms por salto IPSC
  guard_ms: 60            # solapes < guard se absorben

talkgroups:
  - name: FAENA1
    id: "100"             # id DMR numérico del TG
    slot: 1               # TG clavado a slot 1 o 2

repeaters:
  - name: MANQUEHUE
    dmr_id: 741100
    model: slr5500        # preset: slr5500 | slr1000 | xpr8300e
    lat: -33.3555
    lon: -70.4361
    antenna_height_m: 20  # metros sobre el suelo del sitio (AGL)
    tx_freq_mhz: 439.175  # salida (la repetidora transmite aquí)
    rx_freq_mhz: 434.175  # entrada (los radios transmiten aquí)
    peers: [FARELLONES]   # nombres de peers IPSC (se normaliza bidireccional)
    slots: { ts1: true, ts2: true }
    scope: rm-cordillera  # tag de región para el mapa

radios:
  - name: HT-RUTA-VN
    role: handy           # handy | mobile
    model: r7a            # preset: dp4801e | dp4401e | sl7550e | r7a | dm4601e
    lat: -33.3260
    lon: -70.2890
    tgs: [FAENA1, EMERGENCIA]
    # tx_power_w: 5       # override opcional de cualquier campo del preset
    # Un radio sin lat/lon entra al roster pero la simulación lo marca
    # "no_position": explicado, nunca drop silencioso.
```

### Referencia de escenario

```yaml
calls:
  - from: HT-RUTA-VN     # radio que originó la PTT
    tg: FAENA1           # talkgroup pedido
    at_ms: 0             # instante de la PTT, ms desde t=0
    duration_ms: 8000    # cuánto dura presionado el PTT
    # site: MANQUEHUE    # opcional: fuerza el sitio; default = uplink más fuerte
```

Un escenario es una lista de llamadas; el motor las lee TODO de una vez y
resuelve el entrelazado con aritmética de intervalos (determinista, sin
loop de reloj), por eso es barato de correr por click.

---

## 3. Cómo correr

### CLI headless

```bash
# texto legible (default)
go run ./cmd/dmrsim \
  -network dmr-network.yaml \
  -scenario examples/dmr/scenario.example.yaml

# markdown (pegable en un issue) o JSON (scriptable)
go run ./cmd/dmrsim -network dmr-network.yaml -scenario escenario.yaml -md
go run ./cmd/dmrsim -network dmr-network.yaml -scenario escenario.yaml -json
```

Flags: `-config` (yaml del repo, para `terrain.*`), `-network`,
`-scenario`, `-json`, `-md`, `-serve :addr`.

El reporte entrega, por llamada: sitio y slot al que aterrizó, quién
escuchó (origen / sitio local / vía IPSC con número de saltos), la razón
de cada radio que NO escuchó (`no_position`, `site_not_linked`,
`no_margin`, `tg_no_sub`, `denied_busy`, `slot_not_carried`), el estado
de la llamada (`aired` / `denied` / `no_uplink` / `not_carried`), la
marca de audio corrompido (`garbled`) y la carga de canal (duty cycle y
colisiones por sitio y slot). Nunca hay drop silencioso: cada no-entrega
viene explicada.

### Servidor local (sin Docker)

```bash
go run ./cmd/dmrsim -serve :8090
# → http://localhost:8090/dmr.html  (página DMR con mapa Leaflet)
# → http://localhost:8090/api/dmr/topology
```

Sirve `public/` estático + la API DMR: es la misma caja que el
deployment usarás vía Docker, solo que sin planes compartidos.

### Deployment completo (nginx + shareapi + dmrapi)

En Docker (`docker-compose.yml` del repo) con `config.yaml` cuyo
`source.type` es `dmrtopology`: el binario `hopreach-shareapi` registra
las rutas `/api/dmr/*` montando `internal/dmrapi`, y nginx sirve `public/`
(incluida `dmr.html`). El flujo de arranque: nginx (80/443) → static +
proxy `/api/` → shareapi. Si `dmr-network.yaml` está roto, el log dice
`DMR API disabled` y el resto del sitio sigue funcionando (plan
sharing, mapa) — nunca un 500 para todo.

### API HTTP DMR

| Ruta | Verbo | Qué entrega |
|---|---|---|
| `/api/dmr/topology` | GET | La red entera: repetidoras, radios, TGs, política, y **todos** los enlaces up/down con `margin_db`, `path_db`, `distance_km` y clase (`solid` ≥ +9 dB, `ok` ≥ 0, `marginal` ≥ −6, `blocked` < −6). |
| `/api/dmr/simulate` | POST | Un escenario en JSON (el mismo shape `calls` del YAML) → `Report` completo. |
| `/api/dmr/policy` | GET | La política de la red (colisión, guard, latencia IPSC). |

`fspl_mode: true` en la respuesta = **no había DEM disponible**; los
márgenes son espacio-libre puro (optimistas). Ese flag existe para que
nadie confíe en márgenes que no vieron el terreno.

La primera llamada a la API carga y computa el terreno una sola vez por
proceso (los tiles del DEM son inmutables). Si editas `dmr-network.yaml`,
reinicia el servidor para recargar.

---

## 4. Dónde está la física y los presets (mapa del código)

- `internal/propagation` — presupuesto de enlaces paramétrico por
  frecuencia y difracción ITU-R P.526. **Intacta del proyecto original.**
- `internal/demgrid` — grilla de elevación (mmap en unix, heap en
  Windows para el dev-host).
- `internal/dmr/dmr.go` — dominio (`Network`, `Repeater`, `Radio`,
  `Talkgroup`, `Policy`) + presets de equipos Motorola, 2–3 dB
  conservadores por diseño (planning erra del lado seguro).
- `internal/dmr/links.go` — `ComputeLinks`: asociación por uplink más
  fuerte, downlink espejado, clase por margen.
- `internal/dmrsim/engine.go` — motor temporal: ledger de TX por
  (sitio, slot), claim de TG vía IPSC, `markMutualGarble` con guard,
  latencia por salto, duty cycle por ventana.
- `internal/dmrapi/dmrapi.go` — API HTTP DMR (`topology`, `simulate`,
  `policy`) con cache perezoso del DEM.
- `internal/meshsource/dmrsource.go` — backend `dmrtopology` del
  pipeline existente (mapa, roster, región): repetidoras
  `role="repeater"`, radios `role="companion"`, sin paquetes observados.
- `cmd/dmrsim` — CLI headless + `-serve` para desarrollo local.
- `cmd/hopreach-shareapi` — monta dmrapi cuando `source.type` es
  `dmrtopology`.
- `public/dmr.html` + `public/dmr.js` + `public/style-dmr.css` — la
  página DMR (Leaflet).

**Contrato clave**: `propagation.PathMargin` recibe la altura ASL real
del transmisor (terreno + mástil) y al receptor ya elevado
`rx_height_m`/`height_m` sobre su terreno. La antena de un handy a 1,6 m
genera difracción genuina (≈2–3 dB) incluso a campo ridículamente
despejado — esa física viene con el modelo, no hay tuning por mano.

---

## 5. Escenarios tipo (qué probar primero)

1. **Talkgroup cruzado**: dos handys en sitios distintos con el mismo TG
   casi a la vez → el claim de TG por IPSC evita la doble entrada; el
   duty cycle del (sitio, slot) correspondiente sube.
2. **Guard**: una llamada de 120 ms sobre otra activa → absorbe; la
   misma prueba con 400 ms → ambas quedan garbilizadas.
3. **Frontera de margen**: handy en la ruta a Valle Nevado (del ejemplo)
   vs base urbana — la clase `ok`/`marginal`/`blocked` cambia cuando un
   corte de ladera entra en la línea de vista.
4. **Política deny**: dos PTT en solape con `collision: deny` → la
   segunda devuelve `denied_busy` y el ledger de aire no carga la
   segunda.

---

## 6. Cómo adaptarlo a TU red (checklist)

1. Copia `examples/dmr/dmr-network.example.yaml` → `dmr-network.yaml`.
2. Pon tus repetidoras: coordenadas reales de cada sitio, mástil real
   (`antenna_height_m`), par de frecuencias real y peers IPSC reales.
3. Declara tus **talkgroups clavados a slot** (Tier II convencional).
4. Registra tus radios con su suscripción a TGs (un radio que no lleva
   el TG no escucha: `tg_no_sub`).
5. Ajusta `config.yaml`: `terrain.dem_zoom` 12–13 para detalle local,
   `dmr.network_path`, y el límite de región si lo usas (GeoJSON bajo
   `region.boundary_path`).
6. Arranca `go run ./cmd/dmrsim -serve :8090` y prueba la página DMR.

Todo error de tipografía en el YAML termina en un mensaje explícito
(`dmr: repeater "X": unknown model preset "Y"`) — los typos nunca pasan
callados.
