// The agentic loop: OpenAI-compatible in, advice out.
//
// One turn = up to MAX_STEPS model calls. Each step the model may:
//   - call a tool (docs search, screenshot, live DMR API) → we dispatch
//     and feed the result back as a tool message
//   - answer → that's the turn
// The system prompt makes the persona and the corpus's rules explicit
// (answer in the user's language, cite docs, never invent a margin: check
// the live API or say "no lo sé" — the same anti-hallucination posture the
// product's own docs hold: la física no se inventa).

"use strict";

const llm = require("./llm.js");
const store = require("./store.js");
const tools = require("./tools.js");
const ux = require("./ux.js");

const MAX_STEPS = Number(process.env.ASSISTANT_MAX_STEPS || 6);

const SYSTEM_PROMPT = `Eres el asistente técnico en línea de HopReach-DMR, un fork que simula redes Motorola TRBO (DMR Tier II convencional + IP Site Connect) sobre terreno real.

Reglas:
1. Responde SIEMPRE en el idioma del usuario (por defecto español de Chile).
2. Fundamenta cada afirmación sobre el producto en la documentación: usa search_docs primero; cita archivo y encabezado (ej: "docs/DMR_MODE.md — Referencia de dmr-network.yaml").
3. Los datos vivos (topología, márgenes, simulaciones) vienen SIEMPRE de las herramientas get_dmr_topology / run_dmr_simulate contra este despliegue. NUNCA inventes nombres de radios, talkgroups o márgenes: si no lo verificaste, dilo.
4. Cuando el problema sea visual (mapa, enlaces, paneles, banners) o el usuario reporte algo extraño, usa take_screenshot para VER la página real antes de responder.
5. Si algo no está modelado (encriptación, roaming, modo directo), dilo en plano: la honestidad es política del producto.
6. Sé concreto: pasos cortos, comandos exactos, valores YAML listos para pegar.`;

// extractQuery: what to embed for retrieval — the last user text message.
function lastUserText(messages = []) {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m.role === "user") {
      if (typeof m.content === "string") return m.content;
      const t = (m.content || []).find((p) => p.type === "text");
      if (t) return t.text;
    }
  }
  return "";
}

// ragContext(messages) → the retrieval block injected into the system
// message. Lexical retrieval (see store.js for the measured why): the
// corpus is small and labeled; no per-query embedding on the hot path.
async function ragContext(messages) {
  const docs = store.load();
  if (!docs.length) {
    return " (aviso: índice RAG vacío — reindexa con node tools/assistant/index.js)";
  }
  const q = lastUserText(messages);
  if (!q) return "";
  const hits = store.retrieveLexical(docs, q, 5);
  if (!hits.length) return "";
  return (
    "\n\n## Contexto recuperado de la documentación del producto\n" +
    hits.map((h) => `### ${h.source} — ${h.heading} (score ${h.score.toFixed(3)})\n${h.text}`).join("\n\n")
  );
}

// runTurn(req) → {message, usage-like diagnostics} for ONE chat-completion
// request. tools_available respects the caller's tool_choice but never
// invents tool schema the upstream doesn't accept — if the upstream
// chokes on tools, degrade to plain RAG and say so in the turn diagnostics.
async function runTurn(reqBody, meta = {}) {
  const t0 = Date.now();
  const sysText = SYSTEM_PROMPT + (await ragContext(reqBody.messages || []));

  let messages = [{ role: "system", content: sysText }, ...(reqBody.messages || [])];
  let toolErrors = 0;
  let screenshots = 0;
  let toolCalls = 0;
  let upstreamError = false;
  let toolsOn = true; // becomes false if the upstream rejects tools-schema

  for (let step = 0; step < MAX_STEPS; step++) {
    let resp;
    try {
      resp = await llm.chat({
        messages,
        tools: tools.DEFINITIONS,
        tool_choice: reqBody.tool_choice === "none" ? "none" : "auto",
      });
    } catch (e) {
      // Tools unsupported upstream (older llama.cpp shapes, etc.): one
      // documented retry without them, then the turn fails honestly.
      if (step === 0) {
        try {
          resp = await llm.chat({ messages, tools: undefined });
          toolsOn = false;
        } catch (e2) {
          upstreamError = true;
          annotate(e2, meta, t0, { toolCalls, toolErrors, screenshots, friction: 0 });
          throw e2;
        }
      } else {
        upstreamError = true;
        annotate(e, meta, t0, { toolCalls, toolErrors, screenshots, friction: 0 });
        throw e;
      }
    }

    const msg = resp.choices?.[0]?.message;
    if (!msg) { upstreamError = true; throw Object.assign(new Error("upstream sin choices"), { }); }

    if (!msg.tool_calls?.length) {
      recordTurn({ t0, toolCalls, toolErrors, screenshots, upstreamError, reqBody, msg });
      return {
        message: msg,
        tools_offered: toolsOn,
        model_used: llm.pickModel(messages) || llm.MODEL || "assistant",
      };
    }

    messages.push({ role: "assistant", content: msg.content ?? "", tool_calls: msg.tool_calls });
    for (const tc of msg.tool_calls.slice(0, 4)) {
      let args = {};
      try { args = JSON.parse(tc.function?.arguments || "{}"); } catch { args = {}; }
      let out = await tools.dispatch(tc.function.name, args);
      toolCalls++;
      if (typeof out === "object" && out?.role === "tool") {
        if ((out.content || []).some((p) => p.type === "image_url")) screenshots++;
        messages.push({ role: "tool", tool_call_id: tc.id, content: out.content });
      } else {
        messages.push({ role: "tool", tool_call_id: tc.id, content: String(out) });
        if (/Error|No pude|falló|no existe/i.test(String(out).slice(0, 60))) toolErrors++;
      }
    }
  }

  // Step budget exhausted: answer with what we have, honest about it.
  recordTurn({ t0, toolCalls, toolErrors, screenshots, upstreamError, reqBody, msg: null });
  return {
    message: {
      role: "assistant",
      content:
        "Corté el análisis por presupuesto de pasos (asistencia acotada por diseño). Los datos ya cargados arriba son confiables; reformula si quieres profundizar en un punto a la vez.",
    },
    tools_offered: toolsOn,
  };
}

function annotate(e, meta, t0, counters = {}) {
  ux.append({
    ts: new Date().toISOString(),
    latency_ms: Date.now() - t0,
    upstream_error: true,
    ...meta,
    ...counters,
  });
  return e;
}

function recordTurn({ t0, toolCalls, toolErrors, screenshots, upstreamError, reqBody, msg }) {
  const lastUser = reqBody.messages?.filter((m) => m.role === "user").pop();
  const friction = ux.frictionHits(lastUser ? (typeof lastUser.content === "string" ? lastUser.content : (lastUser.content?.find(p => p.type === "text")?.text || "")) : "");
  const ev = {
    ts: new Date().toISOString(),
    session: reqBody.session || reqBody.user || "anon",
    latency_ms: Date.now() - t0,
    tool_calls: toolCalls,
    tool_errors: toolErrors,
    screenshots,
    upstream_error: upstreamError,
    friction,
    chars_in: lastUser ? String(lastUser.content || "").length : 0,
    chars_out: (msg?.content || "").length,
  };
  ev.score = ux.scoreTurn(ev);
  ux.append(ev);
}

module.exports = { runTurn, SYSTEM_PROMPT };
