// Markdown chunker for the RAG.
//
// The corpus is the fork's own documentation (the MD this repo grew):
// README, CHANGELOG, docs/*, examples/* as reference snippets. The chunk
// unit is the *heading* — documentation is written in heading-sized
// answer units (a section answers one question), so heading-aligned
// chunks retrieve whole answers instead of half-sentences.

"use strict";

const fs = require("node:fs");
const path = require("node:path");

// Where the corpus lives relative to the repo root (tools/assistant).
const CORPUS = [
  "README.md",
  "CHANGELOG.md",
  "CLAUDE.md",
  "docs/DMR_MODE.md",
  "docs/TECHNICAL.md",
  "docs/simulator-model.md",
  "docs/TECHNICAL.md",
  "examples/dmr/dmr-network.example.yaml",
  "examples/dmr/scenario.example.yaml",
  "examples/dmr/config.example-dmr.yaml",
];

// Heading regex (md ## or #), used to split; also splits fenced code
// blocks from prose so a YAML snippet lands whole instead of bisected.
function chunkMarkdown(body, file) {
  const lines = body.split(/\r?\n/);
  const chunks = [];
  let cur = { heading: "(inicio del documento)", lines: [] };
  let inFence = false;

  const flush = () => {
    const text = cur.lines.join("\n").trim();
    if (text) chunks.push({ file, heading: cur.heading, text });
    cur.lines = [];
  };

  for (const line of lines) {
    if (/^```/.test(line.trim())) inFence = !inFence;
    const isHeading = !inFence && /^(#{1,4})\s/.test(line);
    if (isHeading) {
      flush();
      cur.heading = line.replace(/^#+\s*/, "").trim();
    }
    cur.lines.push(line);
  }
  flush();
  return chunks;
}

function loadCorpus(repoRoot) {
  const out = [];
  for (const rel of CORPUS) {
    const p = path.join(repoRoot, rel);
    try {
      const body = fs.readFileSync(p, "utf8");
      for (const ch of chunkMarkdown(body, rel)) {
        // Prefix every chunk with its file+heading: retrieval quality in
        // small corpora lives on labels, not vibes.
        out.push({ source: rel, heading: ch.heading, text: ch.text });
      }
    } catch (e) {
      // A missing file is logged at indexing time, not fatal — the corpus
      // still serves from what exists.
      console.warn(`indexer: no está ${rel}: ${e.message.split("\n")[0]}`);
    }
  }
  return out;
}

module.exports = { chunkMarkdown, loadCorpus, CORPUS };
