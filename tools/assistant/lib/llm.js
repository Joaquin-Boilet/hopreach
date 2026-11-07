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

const BASE_URL = process.env.ASSISTANT_BASE_URL || "http://127.0.0.1:11434/v1";
const API_KEY = process.env.ASSISTANT_API_KEY || "";
const MODEL = process.env.ASSISTANT_MODEL || "";
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

// chat() posts a raw OpenAI messages/tools payload and returns the
// response verbatim (choices[].message with .content / .tool_calls).
// Timeout alto A PROPÓSITO: el upstream puede ser un ollama en CPU —
// el primer turno carga los pesos fríos (gemma4:12b ≈ 8 GB ≈ un minuto
// o más en 8 cores) y la respuesta completa puede pasar de varias
// centenas de tokens. Un timeout corto aquí haría parecer caída algo
// que es simplemente hardware honesto.
function chat(body) {
  return postJson("/chat/completions", {
    temperature: 0.3,
    ...body,
    model: body.model || MODEL || "assistant",
  }, 900_000);
}

function embed(input) {
  return postJson("/embeddings", { model: EMBED_MODEL, input }, 400_000);
}

async function listUpstreamModels() {
  const res = await fetch(`${BASE_URL}/models`, {
    headers: API_KEY ? { Authorization: `Bearer ${API_KEY}` } : {},
  });
  const j = await res.json();
  return j.data?.map((m) => m.id).filter(Boolean) || [];
}

module.exports = { BASE_URL, MODEL, EMBED_MODEL, chat, embed, listUpstreamModels, postJson };
