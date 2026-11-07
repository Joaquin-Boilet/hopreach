// Screenshot capability — the agent's eyes.
//
// The user requirement is explicit: the agent MUST be able to see what the
// user sees ("el agente debe poder contar con capturas de pantalla para
// poder dar mejor asistencia"). The app's failure modes are visual (a link
// colour, a misplaced marker, a banner nobody read) — describing pixels
// from memory is how chat support wastes user time. So the agent has one
// tool that boots a real headless Chromium, renders the page the user
// means, and hands the PNG straight to a multimodal model turn.
//
// What is captured:
//   - any URL (default: this deployment's own DMR page)
//   - optionally scrolled / clipped at a CSS selector
//   - plus a short DOM text snapshot as a complement, for models that
//     handle text better than pixels.

"use strict";

const { chromium } = require("@playwright/test");

// One shared browser across turns — launching Chromium is ~200 ms the
// first time and ~50 ms reused, and the deployment opens ONE product.
let _browser = null;

async function browser() {
  if (_browser) return _browser;
  _browser = await chromium.launch({ headless: true });
  return _browser;
}

// capture({url, selector, fullPage, viewportWidth, viewportHeight}) →
// { base64, textSnapshot, url }
async function capture(opts = {}) {
  const url = opts.url || process.env.APP_SCREENSHOT_URL || "http://localhost:8095/dmr.html";
  const b = await browser();
  const ctx = await b.newContext({
    viewport: {
      width: Math.min(Math.max(opts.viewportWidth || 1366, 320), 1920),
      height: Math.min(Math.max(opts.viewportHeight || 900, 480), 1080),
    },
    locale: "es-CL",
  });
  const page = await ctx.newPage();
  const errors = [];
  try {
    page.on("console", (m) => { if (m.type() === "error") errors.push(m.text().slice(0, 200)); });
    await page.goto(url, { waitUntil: "networkidle", timeout: 30_000 });
    await page.waitForTimeout(400); // map tiles settle; honest, not precious

    // Optional: bring a specific element into view / crop to it.
    if (opts.selector) {
      try {
        await page.waitForSelector(opts.selector, { timeout: 5000 });
        await page.locator(opts.selector).first().scrollIntoViewIfNeeded();
      } catch {
        // selector not found IS a finding — report it, don't hide it.
        errors.push(`selector no encontrado: ${opts.selector}`);
      }
    }
    const img = opts.selector && errors.length === 0
      ? await page.locator(opts.selector).first().screenshot()
      : opts.fullPage
        ? await page.screenshot({ fullPage: true })
        : await page.screenshot();

    let textSnapshot = "";
    try {
      textSnapshot = (await page.evaluate(() => document.body.innerText))
        .replace(/\n{3,}/g, "\n\n")
        .slice(0, 6000);
    } catch { /* text snapshot is additive, never load-bearing */ }

    return { base64: img.toString("base64"), url, errors, textSnapshot };
  } finally {
    await page.close();
    await ctx.close();
  }
}

process.on("exit", () => { try { _browser?.close(); } catch { } });

module.exports = { capture };
