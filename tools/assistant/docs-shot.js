// Captura la página DMR del despliegue y documenta 2 estados: mapa
// cargado y reporte de escenario corriendo.
//
//   node tools/assistant/docs-shot.js http://localhost:8095
//
// Además valida que el render real no esté vacío (marcadores presentes) —
// si el mapa sale sin repetidoras, falla: es un test disfrazado de docs.

"use strict";

const { chromium } = require("@playwright/test");

const base = process.argv[2] || "http://localhost:8095";

(async () => {
  const b = await chromium.launch({ headless: true });
  const p = await b.newPage({ viewport: { width: 1440, height: 900 } });
  await p.goto(`${base}/dmr.html`, { waitUntil: "networkidle", timeout: 60000 });
  await p.waitForTimeout(800);

  const nRep = await p.locator(".dmr-marker-rep").count();
  console.log(`marcadores de repetidora: ${nRep}`);
  if (nRep < 3) throw new Error(`solo ${nRep} repetidoras en el mapa — el render miente`);

  await p.screenshot({ path: "docs/screenshot-dmr.png" });

  await p.click("#sc-demo");
  await p.click("#sc-run");
  await p.waitForTimeout(1500);
  const reportVisible = await p.locator("#sc-report").isVisible();
  console.log(`reporte visible: ${reportVisible}`);
  if (!reportVisible) throw new Error("el panel de reporte no apareció tras simular");
  await p.screenshot({ path: "docs/screenshot-dmr-report.png" });
  console.log("docs/screenshot-dmr.png + docs/screenshot-dmr-report.png");
  await b.close();
})().catch((e) => { console.error(`docs-shot ERROR: ${e.message.split("\n")[0]}`); process.exit(1); });
