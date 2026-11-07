// The UX/satisfaction ledger — the product's core indicator, per the
// project brief: "sostener la UX como el indicador principal de grado de
// satisfacción en la experiencia del usuario".
//
// Design:
//   - every assistant turn appends one event (JSONL): latency, tool
//     use, errors, friction signals from the user's own words
//   - a per-turn proxy score (0–100) mixes resolution, responsiveness
//     and friction — honest about being a **proxy**
//   - explicit star ratings (POST /v1/ux/feedback) are recorded beside it
//     and, when present for a turn, are the ground truth that overrides
//     the proxy: the assistant's opinion of itself never outranks the
//     user's
//
// Aggregates ship on GET /v1/ux/metrics — the deployment's satisfaction
// readout, inspectable without a dashboard.

"use strict";

const fs = require("node:fs");
const path = require("node:path");

const DATA_DIR = path.join(__dirname, "..", "data");
const EVENTS_PATH = path.join(DATA_DIR, "ux-events.jsonl");

// Friction signal: the vocabulary of a user starting to lose patience.
const FRICTION = ["no funciona", "error", "falla", "mal", "otra vez", "sigue sin", "tampoco", "??", "¿por qué no", "no sirve"];

function frictionHits(text) {
  const low = String(text || "").toLowerCase();
  return FRICTION.reduce((n, k) => (low.includes(k) ? n + 1 : n), 0);
}

// scoreTurn({latency_ms, tool_calls, tool_errors, upstream_error,
//            friction, screenshots}) → 0..100 proxy.
function scoreTurn(ev) {
  const resolve = ev.upstream_error ? 40 : ev.tool_errors > 0 ? 75 : 100;
  const l = ev.latency_ms ?? 0;
  const speed = l <= 4000 ? 100 : l <= 12000 ? 100 - ((l - 4000) / 8000) * 60 : 40;
  const fr = Math.max(0, 100 - 18 * (ev.friction || 0));
  return Math.round(0.45 * resolve + 0.35 * Math.max(speed, 20) + 0.2 * fr);
}

function append(ev) {
  fs.mkdirSync(DATA_DIR, { recursive: true });
  fs.appendFileSync(EVENTS_PATH, JSON.stringify(ev) + "\n");
}

// readEvents() → [{...}] (last 2000 lines is plenty for a fleet of one).
function readEvents() {
  try {
    const lines = fs.readFileSync(EVENTS_PATH, "utf8").split("\n").filter(Boolean);
    return lines.slice(-2000).map((l) => {
      try { return JSON.parse(l); } catch { return null; }
    }).filter(Boolean);
  } catch {
    return [];
  }
}

// metrics(): satisfaction readout — proxy mean + explicit ratings mean,
// last-24h window and full history. The explicit rating mean is the
// headline number when any exist (users > telemetry).
function metrics() {
  const evs = readEvents();
  const now = Date.now();
  const feedback = readFeedback();
  const win = evs.filter((e) => now - new Date(e.ts).getTime() < 24 * 3600 * 1000);
  const rated = feedback.filter((f) => now - new Date(f.ts).getTime() < 24 * 3600 * 1000);
  const mean = (a) => (a.length ? a.reduce((s, x) => s + x, 0) / a.length : null);
  return {
    turns: evs.length,
    turns_24h: win.length,
    proxy_score_mean: mean(evs.map((e) => e.score)),
    proxy_score_mean_24h: mean(win.map((e) => e.score)),
    upstream_error_rate: evs.length ? evs.filter((e) => e.upstream_error).length / evs.length : 0,
    tool_error_rate: evs.length ? evs.filter((e) => e.tool_errors > 0).length / evs.length : 0,
    screenshot_usage: evs.length ? evs.filter((e) => e.screenshots > 0).length / evs.length : 0,
    feedback_count: feedback.length,
    rating_mean: mean(feedback.map((f) => f.rating)),
    rating_mean_24h: mean(rated.map((f) => f.rating)),
    // The indicator the deployment watches — null until either exists.
    satisfaction: mean(feedback.map((f) => f.rating)) ?? mean(evs.map((e) => e.score)),
    satisfaction_source: feedback.length ? "usuario" : "proxy",
  };
}

// ── explicit feedback (the ground truth) ───────────────────────────────

const FEEDBACK_PATH = path.join(DATA_DIR, "ux-feedback.jsonl");

function appendFeedback(f) {
  fs.mkdirSync(DATA_DIR, { recursive: true });
  fs.appendFileSync(FEEDBACK_PATH, JSON.stringify(f) + "\n");
}

function readFeedback() {
  try {
    return fs.readFileSync(FEEDBACK_PATH, "utf8").split("\n").filter(Boolean).map((l) => {
      try { return JSON.parse(l); } catch { return null; }
    }).filter(Boolean);
  } catch {
    return [];
  }
}

module.exports = { frictionHits, scoreTurn, append, readEvents, metrics, appendFeedback, readFeedback };
