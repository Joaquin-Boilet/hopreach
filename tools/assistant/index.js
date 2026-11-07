// Reindex the RAG corpus: chunk every corpus MD/YAML, embed via the
// OpenAI-compatible embeddings endpoint, persist to data/docs-index.json.
//
//   node tools/assistant/index.js       # full reindex
//   node tools/assistant/index.js -v    # verbose per-batch progress
//
// Re-run after any docs edit. The corpus is the docs the human reads —
// the assistant answers from exactly the same text, so docs drift and
// assistant drift are the same problem caught together.

"use strict";

const path = require("node:path");
const llm = require("./lib/llm.js");
const store = require("./lib/store.js");
const { loadCorpus } = require("./lib/chunker.js");

const verbose = process.argv.includes("-v");
const repoRoot = path.join(__dirname, "..", "..");

(async () => {
  const chunks = loadCorpus(repoRoot);
  const sources = [...new Set(chunks.map((c) => c.source))];
  console.log(`indexer: ${chunks.length} chunks de ${sources.length} fuentes: ${sources.join(", ")}`);

  const docs = [];
  const started = Date.now();
  // Serial batches of 16 — upstreams rate-limit politely and the corpus
  // fits in a handful of them anyway.
  const BATCH = 16;
  for (let i = 0; i < chunks.length; i += BATCH) {
    const batch = chunks.slice(i, i + BATCH);
    const res = await llm
      .embed(batch.map((ch) => `${ch.source} :: ${ch.heading}\n\n${ch.text.slice(0, 4000)}`))
      .catch((e) => ({ error: e }));
    if (res.error) {
      console.error(`indexer: lote ${i}–${i + batch.length - 1} falló: ${res.error.message.split("\n")[0]}`);
      process.exit(1);
    }
    res.data.forEach((emb, j) => {
      docs.push({ ...batch[j], embedding: emb.embedding });
    });
    if (verbose) console.log(`indexer: ${Math.min(i + BATCH, chunks.length)}/${chunks.length} (t=${Math.round((Date.now() - started) / 1000)}s)`);
  }

  store.save(docs);
  console.log(`indexer: ok, ${docs.length} chunks → ${store.INDEX_PATH} en ${Math.round((Date.now() - started) / 1000)}s`);
})();
