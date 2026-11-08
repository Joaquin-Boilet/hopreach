// The vector store: small corpus, big honesty.
//
// The documentation corpus is a few hundred chunks — a managed vector DB
// would be ceremony; a persisted JSON + cosine top-k is the right size and
// is fully inspectable (data/docs-index.json is literally the artifact
// that got retrieved). If the corpus ever outgrows this, the retrieve()
// interface is the seam to swap for Qdrant — call shape unchanged.

"use strict";

const fs = require("node:fs");
const path = require("node:path");

const DATA_DIR = path.join(__dirname, "..", "data");
const INDEX_PATH = path.join(DATA_DIR, "docs-index.json");

// cos(a, b) for normalized-ish float arrays; embeddings from the same
// upstream are consistent, cosine is the standard similarity here.
function cosine(a, b) {
  let dot = 0, na = 0, nb = 0;
  const n = Math.min(a.length, b.length);
  for (let i = 0; i < n; i++) {
    dot += a[i] * b[i];
    na += a[i] * a[i];
    nb += b[i] * b[i];
  }
  if (!na || !nb) return 0;
  return dot / (Math.sqrt(na) * Math.sqrt(nb));
}

function save(docs) {
  fs.mkdirSync(DATA_DIR, { recursive: true });
  fs.writeFileSync(INDEX_PATH, JSON.stringify({ indexed_at: new Date().toISOString(), docs }, null, 1));
}

function load() {
  try {
    const j = JSON.parse(fs.readFileSync(INDEX_PATH, "utf8"));
    return j.docs || [];
  } catch {
    return [];
  }
}

// retrieve(queryVec, k) → top-k {source, heading, text, score}. Pure
// cosine, no reranker: the corpus is small and the chunk labels carry
// most of the precision.
function retrieve(docs, queryVec, k = 6) {
  const scored = docs.map((d) => ({
    source: d.source,
    heading: d.heading,
    text: d.text,
    score: cosine(d.embedding, queryVec),
  }));
  scored.sort((a, b) => b.score - a.score);
  return scored.slice(0, k);
}

// retrieveLexical(query, docs, k) — keyword retrieval for the small
// corpus scale this runs at. Returns the same shape as the vector path.
//
// Why not vectors at query time, measured on the production box (Xeon
// Gold 6138, CPU-only): every query embed cost 13–33 s and forced a
// model swap the ollama scheduler mishandled (5-min load timeouts). This
// corpus is 121 chunks with heading labels — lexical scoring here is
// faster by 100×, deterministic, and model-free. The vector path stays
// available in retrieve() for when the corpus outgrows one lexical pass.
function tokenize(q) {
  return String(q || "").toLowerCase()
    .replace(/[^a-z0-9áéíóúñü_/\-\.\"]+/gi, " ")
    .split(/\s+/).filter((w) => w.length > 1);
}

function retrieveLexical(docs, query, k = 6) {
  const tokens = tokenize(query);
  if (!tokens.length) return [];
  const scored = docs.map((d) => {
    const hay = `${d.source} ${d.heading} ${d.text}`.toLowerCase();
    let score = 0;
    for (const t of tokens) {
      // field-weighted: source/heading hits count far more than body hits
      if (d.source.toLowerCase().includes(t)) score += 3;
      if (d.heading.toLowerCase().includes(t)) score += 2;
      if (d.text.toLowerCase().includes(t)) score += 1;
    }
    return { ...d, score: score / (Math.sqrt(d.text.length) || 1) };
  });
  scored.sort((a, b) => b.score - a.score);
  return scored.filter((s) => s.score > 0).slice(0, k).map((d) => ({
    source: d.source, heading: d.heading, text: d.text, score: d.score / 10,
  }));
}

module.exports = { save, load, retrieve, retrieveLexical, INDEX_PATH };
