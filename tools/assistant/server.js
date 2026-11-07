// The assistant service — an OpenAI-compatible endpoint, per the project
// brief ("usaremos un tipo de API para el servicio compatible con el
// formato de OpenAI para tener mayor compatibilidad").
//
//   POST /v1/chat/completions     the agent: RAG + tools + screenshots
//   POST /v1/embeddings           proxied to the upstream (transparency)
//   GET  /v1/models               the assistant's own model card
//   POST /v1/ux/feedback          explicit user rating (ground truth)
//   GET  /v1/ux/metrics           the satisfaction readout
//
// Any OpenAI-shaped client (openai-python, the OpenAI JS SDK, curl, a DMR
// page chat widget) can point base_url at this and just work.

"use strict";

const http = require("node:http");
const llm = require("./lib/llm.js");
const agent = require("./lib/agent.js");
const tools = require("./lib/tools.js");
const ux = require("./lib/ux.js");
const store = require("./lib/store.js");

const PORT = Number(process.env.ASSISTANT_PORT || 8096);

function json(res, code, body) {
  res.writeHead(code, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(body, null, 2));
}

function readBody(req, limit = 3 * 1024 * 1024) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let n = 0;
    req.on("data", (c) => {
      n += c.length;
      if (n > limit) { reject(Object.assign(new Error("payload too large"), { code: 413 })); req.destroy(); return; }
      chunks.push(c);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

const startedAt = new Date();

// Chat-completion shape the page widget sends: standard OpenAI. The
// `session` field is an extension (any unknown field a client may send is
// ignored upstream anyway); the UX ledger keys on it.
async function handleChat(req, res) {
  let body;
  try {
    body = JSON.parse(await readBody(req));
  } catch (e) {
    return json(res, 400, { error: { message: "JSON inválido: " + e.message, type: "invalid_request_error" } });
  }
  if (!Array.isArray(body.messages)) {
    return json(res, 400, { error: { message: "messages[] requerido", type: "invalid_request_error" } });
  }

  const stream = !!body.stream;
  const meta = { session: body.session || body.user || "anon", client: req.socket.remoteAddress };

  let reply;
  try {
    reply = await agent.runTurn(body, meta);
  } catch (e) {
    return json(res, 502, {
      error: { message: `upstream LLM: ${e.message.split("\n")[0]}`, type: "upstream_error" },
      hint: "revise ASSISTANT_BASE_URL/ASSISTANT_MODEL (endpoint compatible OpenAI)",
    });
  }

  const msg = reply.message;
  const completion = {
    id: `chatcmpl-dmr-${Date.now().toString(36)}`,
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model: reply.model_used || llm.MODEL,
    choices: [{
      index: 0,
      message: msg,
      finish_reason: "stop",
    }],
    usage: { prompt_tokens: -1, completion_tokens: -1, total_tokens: -1 },
    // Extensions (the OpenAI shape tolerates extra keys; the widget renders them):
    hopreach: {
      tools_used: reply.tools_offered,
      ux_satisfaction: ux.metrics().satisfaction,
      docs_corpus: true,
    },
  };

  if (!stream) return json(res, 200, completion);

  // Streaming: the agent loop has already resolved (tool calls need whole
  // turns), so the stream carries the final answer in SSE chunks — same
  // wire format OpenAI streaming clients expect.
  res.writeHead(200, {
    "Content-Type": "text/event-stream; charset=utf-8",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
  });
  const chunk = (delta, finish) =>
    `data: ${JSON.stringify({
      id: completion.id,
      object: "chat.completion.chunk",
      created: completion.created,
      model: llm.MODEL,
      choices: [{ index: 0, delta, finish_reason: finish ?? null }],
    })}\n\n`;

  const text = typeof msg.content === "string" ? msg.content : "";
  res.write(chunk({ role: "assistant", content: "" }));
  for (let i = 0; i < text.length; i += 80) {
    res.write(chunk({ content: text.slice(i, i + 80) }));
  }
  res.write(chunk({}, "stop"));
  res.write("data: [DONE]\n\n");
  res.end();
}

async function handleFeedback(req, res) {
  let body;
  try {
    body = JSON.parse(await readBody(req));
  } catch {
    return json(res, 400, { error: { message: "JSON inválido" } });
  }
  const rating = Number(body.rating);
  if (!Number.isInteger(rating) || rating < 1 || rating > 5) {
    return json(res, 400, { error: { message: "rating debe ser entero 1..5" } });
  }
  ux.appendFeedback({
    ts: new Date().toISOString(),
    session: body.session || "anon",
    rating,
    comment: String(body.comment || "").slice(0, 500),
    page: body.page || "",
  });
  // The satisfaction indicator moves: rating > proxy, always.
  json(res, 200, { ok: true, satisfaction: ux.metrics().satisfaction });
}

const routes = {
  "GET /v1/models": (req, res) =>
    json(res, 200, {
      object: "list",
      data: [{
        id: llm.MODEL || "hopreach-dmr-assistant",
        object: "model",
        owned_by: "hopreach-fork",
        created: Math.floor(startedAt.getTime() / 1000),
      }],
    }),
  "GET /health": (req, res) =>
    json(res, 200, {
      ok: true,
      upstream: llm.BASE_URL,
      model: llm.MODEL || "(ASSISTANT_MODEL sin resolver)",
      embed_model: llm.EMBED_MODEL,
      dmrsim: tools.DMRSIM_URL,
      docs_corpus_chunks: store.load().length,
      uptime_s: Math.round((Date.now() - startedAt.getTime()) / 1000),
    }),
};

const server = http.createServer(async (req, res) => {
  const key = `${req.method} ${req.url.split("?")[0]}`;
  try {
    if (routes[key]) return routes[key](req, res);
    if (req.method === "POST" && req.url === "/v1/chat/completions") return handleChat(req, res);
    if (req.method === "POST" && req.url === "/v1/ux/feedback") return handleFeedback(req, res);
    if (req.method === "GET" && req.url === "/v1/ux/metrics") return json(res, 200, ux.metrics());
    json(res, 404, { error: { message: `ruta desconocida: ${key}` } });
  } catch (e) {
    json(res, 500, { error: { message: e.message.split("\n")[0], type: "internal" } });
  }
});

server.listen(PORT, () => {
  console.log(`assistant: API compatible OpenAI en http://localhost:${PORT}/v1`);
  console.log(`assistant: upstream ${llm.BASE_URL} (modelo ${llm.MODEL || "s/d — set ASSISTANT_MODEL"}) · embeddings ${llm.EMBED_MODEL}`);
  console.log(`assistant: dmrapi en ${tools.DMRSIM_URL} · corpus: node tools/assistant/index.js`);
});
