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

module.exports = { save, load, retrieve, INDEX_PATH };
