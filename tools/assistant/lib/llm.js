// The assistant's upstream LLM client — OpenAI-compatible only, on
// purpose (user requirement: "un tipo de API compatible con el formato de
// OpenAI para tener mayor compatibilidad"). Any OpenAI-shaped backend
// works: Ollama's /v1, vLLM, llamacpp's server, the big commercial ones —
// the code speaks one dialect, base-url decides which brain.
//
// Two endpoints used:
//   POST {base}/chat/completions   (chat, function calling, images)
//   POST {base}/embeddings         (RAG indexing + retrieval)

"use strict";

// Two brains, honest routing (per-turn):
//   ASSISTANT_MODEL        — fast local chat (tools ✓), default qwen3:8b
//   ASSISTANT_VISION_MODEL — multimodal fallback for turns that CARRY an
//                            image (screenshot reads): gemma4:12b. On a
//                            CPU-only upstream this one costs minutes per
//                            turn — used only when pixels arrive.
const BASE_URL = process.env.ASSISTANT_BASE_URL || "http://127.0.0.1:11434/v1";
const API_KEY = process.env.ASSISTANT_API_KEY || "";
const MODEL = process.env.ASSISTANT_MODEL || "";
const VISION_MODEL = process.env.ASSISTANT_VISION_MODEL || MODEL;
const EMBED_MODEL = process.env.ASSISTANT_EMBED_MODEL || "bge-m3";

async function postJson(path, body, timeoutMs = 120_000) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetch(`${BASE_URL}${path}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(API_KEY ? { Authorization: `Bearer ${API_KEY}` } : {}),
      },
      body: JSON.stringify(body),
      signal: ctrl.signal,
    });
    const text = await res.text();
    if (!res.ok) {
      throw Object.assign(new Error(`upstream ${res.status}: ${text.slice(0, 400)}`), { status: res.status });
    }
    return JSON.parse(text);
  } finally {
    clearTimeout(t);
  }
}

// pickModel(messages) — a turn routes to the vision brain only when its
// payload actually carries an image part (screenshot tool results are
// content-parts arrays with image_url).
function pickModel(messages = []) {
  const hasImage = messages.some((m) =>
    Array.isArray(m.content) && m.content.some((p) => p?.type === "image_url"));
  return hasImage ? VISION_MODEL : undefined; // undefined → chat() default
}

// ollamaOption(): extra upstream options, ONLY when this deployment asks
// for them (ASSISTANT_NUM_THREADS). MEASURED on the production box
// (2026-11-08, LXC 144): Ollama's OpenAI shim IGNORES `options` in the
// payload — the runner came up with n_threads=40 (host cores) over an
// 8-CPU cpuset and no turn ever finished. The threads that reach the
// runner live in the MODEL parameters (Modelfile `PARAMETER num_thread 8`,
// deployed as qwen3-8t/gemma4-8t). This passthrough stays for engines
// that do honor in-body options; it is a no-op for Ollama /v1.
function ollamaOptions() {
  const t = Number(process.env.ASSISTANT_NUM_THREADS || 0);
  if (!t || t < 1) return {};
  return { options: { num_thread: t } };
}

// chat() streams the upstream — ALWAYS. Two measured reasons (LXC 144,
// 2026-11-08): (1) a CPU upstream can think for minutes before writing;
// (2) Ollama's OpenAI shim without stream replies only when generation
// ENDS, and Node's fetch (undici) kills the body after ~5 min of silence —
// that is the "upstream LLM: fetch failed" at exactly ~416 s (load +
// tool call + 300 s idle). SSE keeps bytes flowing, so no idle limit
// trips; the whole OpenAI-shaped chat is reassembled below and the
// callers see the same non-stream shape as before.
function chat(body) {
  return postStream("/chat/completions", {
    temperature: 0.3,
    ...body,
    stream: true,
    ...ollamaOptions(),
    model: pickModel(body.messages) || body.model || MODEL || "assistant",
  }, 900_000);
}

// postStream(path, body): OpenAI streaming SSE → one chat.completion
// object (choices[0].message with .content / .reasoning / .tool_calls).
// Covers the shapes the real upstreams emit: Ollama (delta.content,
// delta.reasoning, one-shot tool_calls), llama.cpp / vLLM
// (delta.reasoning_content, incremental tool_calls), commercial ones
// (delta.content + final chunk with usage), all closed by [DONE] or end
// of body. A non-streaming JSON reply (upstream ignored stream:true)
// falls through to plain JSON so nothing regresses.
async function postStream(path, body, timeoutMs = 900_000) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetch(`${BASE_URL}${path}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(API_KEY ? { Authorization: `Bearer ${API_KEY}` } : {}),
      },
      body: JSON.stringify(body),
      signal: ctrl.signal,
    });
    if (!res.ok) {
      const text = await res.text();
      throw Object.assign(new Error(`upstream ${res.status}: ${text.slice(0, 400)}`), { status: res.status });
    }
    const ctype = res.headers.get("content-type") || "";
    if (!ctype.includes("event-stream") || !res.body) return JSON.parse(await res.text());

    const out = { object: "chat.completion", choices: [] };
    const msg = { role: "assistant", content: "", };
    let reasoning = null;
    const toolCalls = new Map();
    let choice = { index: 0, message: msg, finish_reason: null };
    let buf = "";
    const bufReader = sseLines(res.body);
    for await (const line of bufReader) {
      if (!line.startsWith("data:")) continue;
      const data = line.slice(5).trim();
      if (!data || data === "[DONE]") continue;
      let j;
      try { j = JSON.parse(data); } catch { continue; }
      if (j.model && !out.model) out.model = j.model;
      if (j.id && !out.id) out.id = j.id;
      if (j.usage) out.usage = j.usage;
      const d = j.choices?.[0];
      if (!d) continue;
      if (typeof d.delta?.content === "string") msg.content += d.delta.content;
      const think = d.delta?.reasoning ?? d.delta?.reasoning_content;
      if (typeof think === "string") reasoning = (reasoning ?? "") + think;
      if (Array.isArray(d.delta?.tool_calls)) {
        for (const tc of d.delta.tool_calls) {
          const k = Number.isInteger(tc.index) ? tc.index : toolCalls.size;
          const cur = toolCalls.get(k) ||
            { id: tc.id || `call_${k}`, type: "function", function: { name: "", arguments: "" } };
          if (tc.id) cur.id = tc.id;
          if (tc.function?.name) cur.function.name += tc.function.name;
          if (tc.function?.arguments) cur.function.arguments += tc.function.arguments;
          toolCalls.set(k, cur);
        }
      }
      if (d.finish_reason) choice.finish_reason = d.finish_reason;
    }
    if (reasoning !== null) msg.reasoning = reasoning;
    if (toolCalls.size) {
      msg.tool_calls = [...toolCalls.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => v);
    }
    out.choices = [choice];
    return out;
  } finally {
    clearTimeout(t);
  }
}

// sseLines(res.body) → async iterator of complete SSE lines, tolerant to
// chunk boundaries landing mid-line.
async function* sseLines(resBody) {
  const reader = resBody.getReader();
  const dec = new TextDecoder();
  let buf = "";
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let i;
    while ((i = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, i).replace(/\r$/, "");
      buf = buf.slice(i + 1);
      yield line;
    }
  }
  if (buf.trim()) yield buf.replace(/\r$/, "");
}

// embed() posts /embeddings (small request, one body, no SSE — fetch
// semantics fine here).
function embed(input) {
  // options rides as an ollama extension; other upstreams ignore it.
  return postJson("/embeddings", {
    model: EMBED_MODEL,
    input,
    ...ollamaOptions(),
  }, 400_000);
}

async function listUpstreamModels() {
  const res = await fetch(`${BASE_URL}/models`, {
    headers: API_KEY ? { Authorization: `Bearer ${API_KEY}` } : {},
  });
  const j = await res.json();
  return j.data?.map((m) => m.id).filter(Boolean) || [];
}

module.exports = { BASE_URL, MODEL, VISION_MODEL, EMBED_MODEL, chat, embed, listUpstreamModels, postJson, pickModel };
