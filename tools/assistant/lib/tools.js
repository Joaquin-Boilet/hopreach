// The agent's tools — everything it can DO, not just say.
//
// Three families, each mapped to a capability the product actually has:
//   1. search_docs          the RAG (the fork's documentation corpus)
//   2. take_screenshot      headless Chromium over the app (its eyes)
//   3. run_dmr_simulate /   live calls against this deployment's own
//      get_dmr_topology     /api/dmr/* — read what the user's read, run
//                           what the user would run
//
// Tool schema is plain OpenAI function-calling format: any compatible
// backend accepts it (Ollama's /v1, vLLM, llama.cpp server, commercial).

"use strict";

const llm = require("./llm.js");
const store = require("./store.js");
const screenshot = require("./screenshot.js");
const path = require("node:path");
const fs = require("node:fs");

const DMRSIM_URL = process.env.DMRSIM_URL || "http://localhost:8095";
const DATA_PATH = path.join(__dirname, "..", "data");

// ── definitions (OpenAI tools[] shape) ─────────────────────────────────

const DEFINITIONS = [
  {
    type: "function",
    function: {
      name: "search_docs",
      description:
        "Busca en la documentación del producto (README, CHANGELOG, docs técnico y de modo DMR, ejemplos YAML). Usa SIEMPRE esta herramienta antes de responder preguntas de uso, configuración o errores; cita el archivo y encabezado que usaste.",
      parameters: {
        type: "object",
        properties: {
          query: { type: "string", description: "La búsqueda, en el idioma del usuario o en español" },
        },
        required: ["query"],
      },
    },
  },
  {
    type: "function",
    function: {
      name: "take_screenshot",
      description:
        "Abre la aplicación en un navegador headless y captura pantalla (PNG), opcionalmente recortada a un selector CSS. Úsala cuando el usuario reporte algo visual (mapa, enlaces, banner, panel), cuando desverificar una instrución se necesita ver el estado real, o cuando el usuario pegue un screenshot y necesites compararlo. Devuelve la imagen + un snapshot de texto + posibles errores de consola.",
      parameters: {
        type: "object",
        properties: {
          page: { type: "string", description: "Ruta relativa de la página (ej: dmr.html, index.html); vacío = la página DMR por defecto" },
          selector: { type: "string", description: "Selector CSS para recortar (ej: #sc-report, .dmr-legend)" },
          full_page: { type: "boolean", description: "Página completa de arriba abajo (default: sóla el viewport)" },
        },
      },
    },
  },
  {
    type: "function",
    function: {
      name: "run_dmr_simulate",
      description:
        "Corre un escenario DMR real contra este despliegue (POST /api/dmr/simulate). Recibe la lista de llamadas [{from, tg, at_ms, duration_ms, site?}] con nombres EXACTOS de radios y talkgroups de la topología (veríficalos antes con run_dmr_simulate solo después de get_dmr_topology). Devuelve el reporte completo: quién escuchó, razones, carga por slot.",
      parameters: {
        type: "object",
        properties: {
          calls: {
            type: "array",
            items: {
              type: "object",
              properties: {
                from: { type: "string" },
                tg: { type: "string" },
                at_ms: { type: "number" },
                duration_ms: { type: "number" },
                site: { type: "string" },
              },
              required: ["from", "tg", "at_ms", "duration_ms"],
            },
          },
        },
        required: ["calls"],
      },
    },
  },
  {
    type: "function",
    function: {
      name: "get_dmr_topology",
      description:
        "Descarga la topología actual de este despliegue: repetidoras, radios con posición, talkgroups, política, y TODOS los enlaces con margen dB y clase (solid/ok/marginal/blocked). Úsala para responder '¿quién escucha a quién?', '¿qué sitio sirve a X?' o para elegir llamadas válidas antes de simular.",
      parameters: { type: "object", properties: {} },
    },
  },
];

// ── dispatcher ─────────────────────────────────────────────────────────

// Every tool returns OpenAI tool-message content: either a string, or a
// content-parts array when it carries an image (the screenshot case).
async function dispatch(name, args) {
  switch (name) {
    case "search_docs": {
      const docs = store.load();
      if (!docs.length) {
        return "El índice RAG está vacío: reindexa con `node tools/assistant/index.js` (o avisa que este despliegue no indexó docs todavía).";
      }
      const q = await llm.embed(String(args.query || "")).catch((e) => ({ error: e }));
      if (q.error) return `Error buscando en docs: ${q.error.message.split("\n")[0]}`;
      const hits = store.retrieve(docs, q.data[0].embedding, 6);
      return hits
        .map((h) => `### ${h.source} — ${h.heading}\n(score ${h.score.toFixed(3)})\n${h.text}`)
        .join("\n\n---\n\n");
    }

    case "take_screenshot": {
      const url = (args.page || "dmr.html").startsWith("http")
        ? args.page
        : `${DMRSIM_URL}/${(args.page || "dmr.html").replace(/^\/+/, "")}`;
      try {
        const cap = await screenshot.capture({
          url,
          selector: args.selector,
          fullPage: !!args.full_page,
        });
        return {
          role: "tool",
          tool_call_id: null, // agent loop fills it
          content: [
            {
              type: "text",
              text:
                `Captura de ${cap.url} (${cap.base64.length} bytes PNG).` +
                (cap.errors.length ? ` Consola: ${cap.errors.join(" | ")}` : " Consola sin errores.") +
                (cap.textSnapshot ? `\nSnapshot de texto:\n${cap.textSnapshot.slice(0, 3000)}` : ""),
            },
            { type: "image_url", image_url: { url: `data:image/png;base64,${cap.base64}` } },
          ],
        };
      } catch (e) {
        return `No pude capturar ${url}: ${e.message.split("\n")[0]}. ¿Está levantado dmrsim -serve o el contenedor?`;
      }
    }

    case "run_dmr_simulate": {
      try {
        const res = await fetch(`${DMRSIM_URL}/api/dmr/simulate`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ calls: args.calls || [] }),
        });
        const body = await res.json();
        if (!res.ok) return `Error HTTP ${res.status}: ${JSON.stringify(body).slice(0, 400)}`;
        return JSON.stringify(body); // the Report itself
      } catch (e) {
        return `No alcance la API (${DMRSIM_URL}): ${e.message.split("\n")[0]}`;
      }
    }

    case "get_dmr_topology": {
      try {
        const res = await fetch(`${DMRSIM_URL}/api/dmr/topology`);
        const body = await res.json();
        if (!res.ok) return `Error HTTP ${res.status}: ${JSON.stringify(body).slice(0, 400)}`;
        // Trim the payload the model doesn't need raw: keep names, classes
        // and counts, drop duplicated geometry doubles.
        return JSON.stringify({
          network_name: body.network_name,
          fspl_mode: body.fspl_mode,
          policy: body.policy,
          talkgroups: body.talkgroups,
          repeaters: body.repeaters,
          radios: body.radios,
          uplinks: body.uplinks,
          downlinks: body.downlinks,
          serving_of: body.serving_of,
        });
      } catch (e) {
        return `No alcance la API (${DMRSIM_URL}): ${e.message.split("\n")[0]}`;
      }
    }

    default:
      return `Herramienta desconocida: ${name}`;
  }
}

module.exports = { DEFINITIONS, dispatch, DMRSIM_URL };
