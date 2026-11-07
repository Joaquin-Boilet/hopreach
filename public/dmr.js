// DMR page (public/dmr.html) — the MotoTRBO simulation page.
//
// Talks ONLY the DMR API (internal/dmrapi): /api/dmr/topology for the
// network + terrain-computed links, /api/dmr/simulate for running a
// scenario per click. No MeshCore vocabulary in sight — this page speaks
// repeaters, talkgroups, slots and margins, which is what the model
// actually computes.
//
// The link class colouring mirrors internal/dmr's classify():
//   solid ≥ +9 dB · ok ≥ 0 · marginal ≥ −6 · blocked < −6

"use strict";

/* ── state ───────────────────────────────────────────────────────────── */

let topo = null;          // /api/dmr/topology response
let calls = [];           // scenario rows: {from, tg, at_ms, duration_ms, site}
let callLayers = new Map(); // call id -> Leaflet layer group (animation pins)

const marginClass = (m) => m >= 9 ? "solid" : m >= 0 ? "ok" : m >= -6 ? "marginal" : "blocked";
const classColor = { solid: "#2e9e57", ok: "#c9b433", marginal: "#d97e26", blocked: "#d13c3c" };

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
})[c]);

const map = L.map("dmr-map", { preferCanvas: true });

/* ── boot ────────────────────────────────────────────────────────────── */

(async function boot() {
  L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: "© OpenStreetMap contributors",
  }).addTo(map);

  try {
    const res = await fetch("/api/dmr/topology");
    if (!res.ok) throw await fetchErr(res);
    topo = await res.json();
  } catch (e) {
    $("dmr-fspl-banner").hidden = true;
    $("dmr-net-name").innerHTML = `Error al cargar la red <small>${esc(e.message || e)}</small>`;
    return;
  }

  if (topo.fspl_mode) $("dmr-fspl-banner").hidden = false;
  $("dmr-net-name").innerHTML = `${esc(topo.network_name)} <small>DMR Tier II + IPSC</small>`;

  fitMap();
  drawRepeaters();
  drawLinks();
  drawRadios();
  fillRoster();
  fillSelects();
})();

function fetchErr(res) {
  return res.json().then(
    (j) => new Error(j.error || `HTTP ${res.status}`),
    () => new Error(`HTTP ${res.status}`));
}

/* ── map drawing ─────────────────────────────────────────────────────── */

function fitMap() {
  const pts = [];
  for (const r of topo.repeaters) pts.push([r.lat, r.lon]);
  for (const rd of topo.radios) if (rd.lat != null) pts.push([rd.lat, rd.lon]);
  if (pts.length) map.fitBounds(L.latLngBounds(pts).pad(0.18));
  else map.setView([-33.42, -70.38], 11);
}

function repIcon(r) {
  return L.divIcon({
    className: "",
    html: `<div class="dmr-marker-rep"></div>`,
    iconSize: [16, 16],
    iconAnchor: [8, 8],
  });
}

function drawRepeaters() {
  for (const r of topo.repeaters) {
    const slots = (r.slots || [])
      .map((s) => "TS" + s)
      .join(" + ");
    L.marker([r.lat, r.lon], { icon: repIcon(r), zIndexOffset: 400 })
      .bindPopup(
        `<b class="rep-name">${esc(r.name)}</b> — ${esc(r.model)}<br>` +
          `DMR ID ${r.dmr_id} · ${r.tx_freq_mhz} / ${r.rx_freq_mhz} MHz<br>` +
          `${esc(slots)} · ${r.tx_power_w} W · ${r.antenna_gain_dbi} dBi · mástil ${r.mast_height_m} m<br>` +
          (r.peers?.length ? `IPSC: ${esc(r.peers.join(", "))}` : "sin IPSC (sitio aislado)") +
          (r.scope ? `<br>scope ${esc(r.scope)}` : ""))
      .addTo(map);
  }
}

function drawLinks() {
  // Uplinks solid, downlinks dashed, coloured by margin class.
  const draw = (ls, dashed) => {
    for (const l of ls) {
      const r = topo.repeaters.find((x) => x.name === l.repeater);
      const d = topo.radios.find((x) => x.name === l.radio);
      if (!r || !d?.lat) continue;
      const col = classColor[marginClass(l.margin_db)];
      L.polyline(
        [[r.lat, r.lon], [d.lat, d.lon]],
        { color: col, opacity: 0.55, weight: 1.6, dashArray: dashed ? "3 6" : undefined }
      )
        .bindPopup(
          `${dashed ? "Downlink" : "Uplink"} ${esc(l.repeater)} ↔ ${esc(l.radio)}<br>` +
            `margen <b>${l.margin_db.toFixed(1)} dB</b> (${marginClass(l.margin_db)})<br>` +
            `pérdida ${l.path_db.toFixed(1)} dB · ${l.distance_km.toFixed(1)} km`)
        .addTo(map);
    }
  };
  draw(topo.uplinks || [], false);
  draw(topo.downlinks || [], true);
}

function radioDot(rd) {
  const cls = !rd.lat
    ? "dmr-dot nofix"
    : `dmr-dot ${rd.role === "mobile" ? "mobile" : "handy"}`;
  return L.divIcon({
    className: "",
    html: `<div class="${cls}"></div>`,
    iconSize: [12, 12],
    iconAnchor: [6, 6],
  });
}

function drawRadios() {
  for (const rd of topo.radios) {
    if (!rd.lat) continue; // roster card lists them; no marker without a fix
    L.marker([rd.lat, rd.lon], { icon: radioDot(rd), zIndexOffset: 300 })
      .bindPopup(
        `<b>${esc(rd.name)}</b> (${esc(rd.role)}) · ${esc(rd.model)}<br>` +
          `${rd.tx_power_w} W · home ${esc(rd.home_site || "—")}` +
          (rd.tgs?.length ? `<br>TGs: ${esc(rd.tgs.join(", "))}` : "<br>lleva todos los TGs"))
      .addTo(map);
  }
}

/* ── roster ──────────────────────────────────────────────────────────── */

function fillRoster() {
  const chips = (topo.talkgroups || [])
    .map((tg) => `<span class="tg-chip"><b>TS${tg.slot || "-"}</b>${esc(tg.name)} <i>(id ${esc(tg.id)})</i></span>`)
    .join("");
  $("tg-list").innerHTML = chips || "sin talkgroups declarados";

  $("radio-roster").querySelector("tbody").innerHTML = topo.radios
    .map((rd) => `
      <tr>
        <td>${esc(rd.name)}${rd.lat == null ? ' <span class="lg nofix">·</span>' : ""}</td>
        <td>${esc(rd.model)}</td>
        <td>${esc(rd.home_site || "—")}</td>
        <td>${esc((rd.tgs || []).join(", ") || "todos")}</td>
      </tr>`)
    .join("");
}

/* ── scenario builder ────────────────────────────────────────────────── */

function fillSelects() {
  $("sc-radio").innerHTML = topo.radios.map((rd) =>
    `<option value="${esc(rd.name)}">${esc(rd.name)}${rd.lat == null ? " (sin posición)" : ""}</option>`).join("");
  $("sc-tg").innerHTML = (topo.talkgroups || []).map((tg) =>
    `<option value="${esc(tg.name)}">TS${tg.slot || "?"} · ${esc(tg.name)}</option>`).join("");
}

function renderCalls() {
  $("sc-calls").querySelector("tbody").innerHTML = calls
    .map((c, i) => `
      <tr>
        <td>${i + 1}</td>
        <td>${esc(c.from)}</td>
        <td>${esc(c.tg)}</td>
        <td>${c.at_ms}</td>
        <td>${c.duration_ms}</td>
        <td><button class="call-del" data-i="${i}" title="quitar">✕</button></td>
      </tr>`)
    .join("") || `<tr><td colspan="6" style="opacity:.5">— sin llamadas —</td></tr>`;

  document.querySelectorAll(".call-del").forEach(b => {
    b.onclick = () => { calls.splice(Number(b.dataset.i), 1); renderCalls(); };
  });
}

$("sc-add").onclick = () => {
  const from = $("sc-radio").value;
  const tg = $("sc-tg").value;
  const at_ms = Math.max(0, Number($("sc-at").value) || 0);
  const duration_ms = Math.max(250, Number($("sc-dur").value) || 3000);
  if (!from || !tg) return status("faltan radio o talkgroup", false);
  calls.push({ from, tg, at_ms, duration_ms });
  $("sc-at").value = at_ms + duration_ms;
  renderCalls();
};

$("sc-clear").onclick = () => { calls = []; renderCalls();
  $("sc-report").hidden = true; clearCallLayers(); status(null); };

// The bundled example scenario (examples/dmr/scenario.example.yaml), so a
// fresh deployment demonstrates overlap + guard + TG filtering in one run.
$("sc-demo").onclick = () => {
  if (!topo) return;
  const radio = (n) => topo.radios.some((r) => r.name === n) ? n : topo.radios[0]?.name;
  const tg = (n) => (topo.talkgroups || []).some((t) => t.name === n) ? n : topo.talkgroups?.[0]?.name;
  calls = [
    { from: radio("HT-RUTA-VN"), tg: tg("FAENA1"), at_ms: 0, duration_ms: 8000 },
    { from: radio("HT-MAIPO"), tg: tg("MANTENCION"), at_ms: 1500, duration_ms: 4000 },
    { from: radio("BASE-FLOTA"), tg: tg("EMERGENCIA"), at_ms: 7000, duration_ms: 6000 },
  ].filter((c) => c.from && c.tg);
  renderCalls();
};

$("sc-run").onclick = async () => {
  status(null);
  if (!calls.length) return status("no hay llamadas: agrega al menos una", false);
  const btn = $("sc-run");
  btn.disabled = true;
  clearCallLayers();
  try {
    const res = await fetch("/api/dmr/simulate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ calls }),
    });
    const body = await res.json();
    if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
    renderReport(body);
    clearCallLayers();
    animateReport(body);
  } catch (e) {
    status("error: " + (e.message || e), false);
  } finally {
    btn.disabled = false;
  }
};

function status(msg, ok = true) {
  const el = $("sc-status");
  el.hidden = !msg;
  el.textContent = msg || "";
  el.classList.toggle("ok", ok && !!msg);
}

/* ── report rendering ────────────────────────────────────────────────── */

const badgeFor = (s) =>
  s === "aired" ? "al aire" :
  s === "denied" ? "denegada" :
  s === "no_uplink" ? "sin uplink" :
  s === "not_carried" ? "slot no llevado" : s;

const routeLine = (d) => {
  if (d.route === "caller") return "origen";
  if (d.route === "local") return "escucha local";
  if (d.route === "ipsc") return `vía IPSC · ${d.hops} salto${d.hops === 1 ? "" : "s"}`;
  switch (d.reason) {
    case "no_position": return "sin posición";
    case "site_not_linked": return "su sitio no recibió el TG";
    case "no_margin": return "sin margen dB";
    case "tg_no_sub": return "no está suscrito";
    case "denied_busy": return "denegado (ocupado)";
    default: return d.reason || "—";
  }
};

function renderReport(rep) {
  if (rep.fspl_mode) $("dmr-fspl-banner").hidden = false;

  const callsEl = (rep.calls || []).map((c) => `
    <div class="report-call">
      <div class="rc-head">
        <div>
          <span class="rc-title">#${c.id} ${esc(c.from)} → ${esc(c.tg)}</span><br>
          <span class="rc-meta">${esc(c.site)} · TS${c.slot} · ${c.at_ms}–${c.end_ms} ms</span>
        </div>
        <span class="rc-badge ${c.status}">${badgeFor(c.status)}${c.garbled ? " · solape" : ""}</span>
      </div>
      <ul class="rc-deliv">
        ${(c.deliveries || []).map((d) => `
          <li class="${d.delivered ? "" : "no"}">
            <span class="dot" style="background:${d.delivered ? (d.garbled ? "var(--dmr-marg)" : "var(--dmr-solid)") : "var(--dmr-blk)"}"></span>
            ${esc(d.radio)}
            <em>${d.delivered ? routeLine(d) : routeLine(d)}${d.garbled ? " (audio parcialmente corrupto)" : ""}</em>
          </li>`).join("")}
      </ul>
    </div>`).join("");
  $("report-calls").innerHTML = callsEl;

  $("report-loads").innerHTML = (rep.site_loads || []).map((l) => `
    <div class="load-row">
      <div class="load-label">
        <span>${esc(l.repeater)} · TS${l.slot}</span>
        <span>${l.transmit_ms} ms · duty ${l.duty_cycle.toFixed(1)}%${l.collisions ? ` · ${l.collisions} colisiones` : ""}</span>
      </div>
      <div class="load-track">
        <div class="load-fill ${l.slot === 2 ? "ts2" : "ts1"}" style="width:${Math.min(100, l.duty_cycle)}%"></div>
      </div>
    </div>`).join("") || `<p style="opacity:.5; font-size:.75rem">sin carga: ninguna llamada al aire</p>`;

  $("sc-report").hidden = false;
}

/* ── call animation on the map ───────────────────────────────────────── */

function clearCallLayers() {
  for (const g of callLayers.values()) map.removeLayer(g);
  callLayers = new Map();
}

// animateReport draws each call as a highlighted marker at the keying site
// with its (site, slot) tag, plus listener count badges. Simple and honest
// — the physics numbers live in the popup cards, not in choreography.
function animateReport(rep) {
  for (const c of rep.calls || []) {
    const g = L.layerGroup().addTo(map);
    const r = topo.repeaters.find((x) => x.name === c.site);
    if (!r) continue;
    const nHeard = (c.deliveries || []).filter((d) => d.delivered && d.route !== "caller").length;
    const col = c.status === "aired" ? (c.garbled ? "#d97e26" : "#5b8def")
              : c.status === "denied" ? "#d13c3c" : "#d97e26";
    // Calls keying the same site stack their labels; offset by id so both
    // stay readable (a badge on top of another badge is friction).
    const dx = 0, dy = ((c.id - 1) % 5) * 20;
    L.marker([r.lat, r.lon], {
      icon: L.divIcon({
        className: "",
        html: `<div class="dmr-call-label" style="background:${col};transform:translate(${dx}px,${dy}px)">TS${c.slot} · ${nHeard} oyentes</div>`,
        iconSize: null, iconAnchor: [8, 8],
      }),
      zIndexOffset: 900,
    }).addTo(g);
    callLayers.set(c.id, g);
  }
}
