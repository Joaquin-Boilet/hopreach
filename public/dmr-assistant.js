// Widget del asistente (página DMR): panel de chat contra el servicio
// agéntico (API compatible OpenAI, /v1/chat/completions). En este
// deployment nginx la proxia; en dev sin asistente, se declara honesto:
// el botón avisa y se desvanece en vez de fingir chat roto.
//
// La satisfacción es el indicador principal del producto: cada respuesta
// trae la fila de estrellas 1..5 que va directo a POST /v1/ux/feedback —
// la la nota del usuario anula al score proxy del servicio.

"use strict";

(function () {
  const $ = (id) => document.getElementById(id);
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c]);

  // session persistente (anónima): el ledger de UX la agrupa.
  let session = localStorage.getItem("dmr-asst-session");
  if (!session) {
    session = "s" + Math.random().toString(36).slice(2, 10);
    localStorage.setItem("dmr-asst-session", session);
  }

  // Base del servicio: en el deployment nginx la proxia en /v1. En dev sin
  // nginx, el puerto directo del asistente si está enlocalhost (convenio
  // de dmrapi/dmrsim: 18096). Una sonda de arranque deja el base resuelto.
  let API = "/v1/chat/completions";
  async function resolveApi() {
    try {
      const r = await fetch("/v1/models", { method: "GET" });
      if (r.ok) return;
      throw new Error();
    } catch {
      if (location.hostname === "localhost" || location.hostname === "127.0.0.1") {
        try {
          const r = await fetch("http://localhost:18096/v1/models");
          if (r.ok) API = "http://localhost:18096/v1/chat/completions";
        } catch { /* queda /v1: el error de la primera pregunta avisará */ }
      }
    }
  }

  const history = []; // [{role, content}] solo texto (widget plano)

  function openPanel() {
    const p = $("asst-panel");
    p.hidden = !p.hidden;
    if (!p.hidden) $("asst-input").focus();
  }

  function pushMsg(role, content, rateable = false) {
    const div = document.createElement("div");
    div.className = `asst-msg ${role}`;
    div.innerHTML = content.startsWith("<") ? content : `<div class="asst-body">${content}</div>`;
    if (rateable) {
      const row = document.createElement("div");
      row.className = "asst-rate";
      row.innerHTML = `
        <span>¿te sirvió?</span>
        ${[1, 2, 3, 4, 5].map((r) => `<button data-r="${r}" title="nota ${r}/5" class="asst-star">★</button>`).join("")}`;
      row.querySelectorAll(".asst-star").forEach((b) => {
        b.onclick = async () => {
          row.querySelectorAll(".asst-star").forEach((x) => (x.style.color = ""));
          for (let i = 0; i < Number(b.dataset.r); i++) row.querySelectorAll(".asst-star")[i].style.color = "#e2b93b";
          try {
            await fetch("/v1/ux/feedback", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ session, rating: Number(b.dataset.r), page: "dmr" }),
            });
            row.innerHTML = `<span class="asst-thanks">anotado — gracias</span>`;
          } catch { row.innerHTML = `<span>no se pudo registrar la nota</span>`; }
        };
      });
      div.appendChild(row);
    }
    $("asst-msgs").appendChild(div);
    $("asst-msgs").scrollTop = 1e9;
    return div;
  }

  async function send() {
    const text = $("asst-input").value.trim();
    if (!text) return;
    $("asst-input").value = "";
    pushMsg("user", esc(text));
    history.push({ role: "user", content: text });

    const busy = pushMsg("assistant", `<div class="asst-body asst-thinking">…</div>`);
    try {
      const res = await fetch(API, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          session,
          messages: history.slice(-12),
        }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error?.message || `HTTP ${res.status}`);
      const reply = body.choices?.[0]?.message?.content || "(respuesta vacía)";
      history.push({ role: "assistant", content: reply });
      busy.querySelector(".asst-body").innerHTML = mdLite(reply);
      pushMsgIfNoRow(busy, true);
    } catch (e) {
      busy.querySelector(".asst-body").innerHTML =
        `El asistente no está disponible ahora: <b>${esc(e.message)}</b>. ` +
        `En este despliegue se apaga con la variable ASSISTANT_ del servicio (nada que configurar acá).`;
    }
  }

  // after-content rating row (pushMsg's rateable path reused inline)
  function pushMsgIfNoRow(busy, rateable) {
    const hasRow = busy.querySelector(".asst-rate");
    if (hasRow || !rateable) return;
    const row = document.createElement("div");
    row.className = "asst-rate";
    row.innerHTML = `
      <span>¿te sirvió?</span>
      ${[1, 2, 3, 4, 5].map((r) => `<button data-r="${r}" title="nota ${r}/5" class="asst-star">★</button>`).join("")}`;
    row.querySelectorAll(".asst-star").forEach((b) => {
      b.onclick = async (ev) => {
        const r = Number(b.dataset.r);
        try {
          await fetch("/v1/ux/feedback", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ session, rating: r, page: "dmr" }),
          });
          row.innerHTML = `<span class="asst-thanks">anotado (${r}/5) — gracias</span>`;
        } catch { row.innerHTML = `<span>no se pudo registrar la nota</span>`; }
      };
    });
    busy.appendChild(row);
  }

  // mdLite: bold, code, bullets — sin importar una lib de markdown.
  function mdLite(t) {
    const lines = esc(t).split("\n");
    const out = [];
    let inCode = false;
    for (const l of lines) {
      if (/^```/.test(l)) { inCode = !inCode; out.push(inCode ? '<pre class="asst-code">' : "</pre>"); continue; }
      if (inCode) { out.push(l + "\n"); continue; }
      out.push(
        l
          .replace(/\*\*(.+?)\*\*/g, "<b>$1</b>")
          .replace(/`([^`]+)`/g, '<code class="asst-inline">$1</code>')
          .replace(/^### (.*)/, "<b>$1</b>")
          .replace(/^## (.*)/, "<b>$1</b>")
          .replace(/^- (.*)/, "• $1") + "\n",
      );
    }
    return out.join("");
  }

  // boot: botón flotante + listeners
  window.addEventListener("DOMContentLoaded", () => {
    resolveApi();
    $("asst-open").onclick = openPanel;
    $("asst-send").onclick = send;
    $("asst-input").addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); }
    });
    pushMsg("assistant", `Hola — soy el asistente de este despliegue DMR. Pregúntame por la red, los enlaces o corre un escenario conmigo.`); // no rateable
    $("asst-close").onclick = openPanel; // mismo botón: alterna
    // Si el servicio no existe en este despliegue, la primera pregunta lo
    // dirá — el botón igual se queda: la nota de UX es parte del producto.
  });
})();
