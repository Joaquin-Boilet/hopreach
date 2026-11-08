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
// for them (ASSISTANT_NUM_THREADS). Why it matters: an LXC's llama-server
// reads the HOST's /proc/cpuinfo (80 cores) and spawns that many threads
// over the container's 8-CPU cpuset — thread contention collapsed prefill
// to 0.31 tok/s and every generate hung past 420 s. Passing num_thread=8
// fixed it: 30 s cold including model load. Other OpenAI-shaped upstreams
// ignore the unknown field, so the gate stays opt-in.
function ollamaOptions() {
  const t = Number(process.env.ASSISTANT_NUM_THREADS || 0);
  if (!t || t < 1) return {};
  return { options: { num_thread: t } };
}

// chat() posts a raw OpenAI messages/tools payload and returns the
// response verbatim (choices[].message with .content / .tool_calls).
// Timeout alto A PROPÓSITO: el upstream puede ser un ollama en CPU —
// el primer turno carga los pesos fríos y la respuesta completa puede
// pasar de varias centenas de tokens. Un timeout corto aquí haría
// parecer caída algo que es simplemente hardware honesto.
function chat(body) {
  return postJson("/chat/completions", {
    temperature: 0.3,
    ...body,
    ...ollamaOptions(),
    model: pickModel(body.messages) || body.model || MODEL || "assistant",
  }, 900_000);
}

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
