# Asistente agéntico HopReach-DMR (RAG sobre la documentación propia)

Un asistente en línea que asesora al usuario **en todo momento**, generado
sobre la misma documentación que lee el mantenedor: los MD del fork
(README, CHANGELOG, `docs/DMR_MODE.md`, `docs/TECHNICAL.md`, ejemplos YAML)
son el corpus RAG. Docs y asistente no pueden desincronizarse porque
comparten texto fuente — se reindexa y ambos quedan iguales.

## Lo que hace el agente

| Capacidad | Herramienta | Fuente |
|---|---|---|
| Responder desde los docs, citando archivo+encabezado | `search_docs` | `data/docs-index.json` (reindex: `node tools/assistant/index.js`) |
| **Ver la pantalla** del usuario (captura real) | `take_screenshot` | Playwright Chromium headless sobre la página del despliegue |
| Operar el producto como lo haría el usuario | `run_dmr_simulate`, `get_dmr_topology` | `/api/dmr/*` del despliegue (mismo dmrsim/dmrapi que la UI) |

La captura de pantalla es una política, no un lujo: los problemas del
producto son visuales (clase de un enlace, banner FSPL, panel de escenarios)
y describir píxeles de memoria es como el soporte pierde tiempo.

## La satisfacción UX como indicador principal

Cada turno se registra en `data/ux-events.jsonl`: latencia, uso de
herramientas, errores, señales de fricción (las palabras del usuario mismo)
y un **score proxy 0–100** (`lib/ux.js`). La calificación explícita
(`POST /v1/ux/feedback`, estrellas 1–5) convive en `data/ux-feedback.jsonl`
y, cuando existe, **anula al proxy**: la opinión del usuario supera a la
telemetría. El número visible es `satisfaction` en `GET /v1/ux/metrics`.

## API compatible con OpenAI (por diseño)

Base URL del servicio:

```
POST /v1/chat/completions   # agente (RAG + tools + screenshots)
POST /v1/ux/feedback        # rating 1..5 + comentario
GET  /v1/ux/metrics         # satisfacción agregada
GET  /v1/models             # la ficha del modelo del asistente
GET  /health                # estado del upstream + corpus
```

Cualquier cliente OpenAI-shape (SDKs python/JS, curl, chat widget) apunta
`base_url` a este servicio y funciona: es el formato completo —
messages[], tool_calls, respuesta `chat.completion`, streaming SSE del
texto final (el loop agéntico resuelve antes; el stream lleva la respuesta
ya decidida).

## Configuración (variables de entorno)

| Var | Default | Qué es |
|---|---|---|
| `ASSISTANT_BASE_URL` | `http://127.0.0.1:11434/v1` | **Upstream LLM compatible OpenAI** (Ollama `/v1`, vLLM, llama-server, comercial) |
| `ASSISTANT_API_KEY` | — | Solo si el upstream pide token |
| `ASSISTANT_MODEL` | — | Modelo de chat del upstream (ej `qwen2.5:14b`) |
| `ASSISTANT_EMBED_MODEL` | `bge-m3` | Modelo de embeddings para el RAG |
| `DMRSIM_URL` | `http://localhost:8095` | Dónde vive la API DMR (`dmrsim -serve` o el contenedor) |
| `APP_SCREENSHOT_URL` | `http://localhost:8095/dmr.html` | Página que captura por defecto |
| `ASSISTANT_PORT` | `8096` | Puerto de este servicio |

## Cómo correr

```bash
# 1) índice del RAG (después de editar cualquier MD)
node tools/assistant/index.js -v

# 2) servicio
ASSISTANT_BASE_URL=http://<ollama-host>:11434/v1 \
ASSISTANT_MODEL=qwen2.5:14b \
DMRSIM_URL=http://localhost:8095 \
node tools/assistant/server.js

# 3) probar
curl http://localhost:8096/v1/chat/completions -H "content-type: application/json" \
  -d '{"session":"prueba","messages":[{"role":"user","content":"¿cómo declaro un talkgroup en TS2 y qué pasa si la repetidora no lleva ese slot?"}]}'
```

En el deployment (Proxmox LXC): el servicio corre como un binario Node
autónomo, nginx hace proxy de `/v1/` hacia él y de `/dmr.html` hacia
dmrsim/shareapi — misma caja, dos procesos.

## Qué NO es (honestidad)

- No es memoria de largo plazo: cada turno es sin estado salvo la sesión
  que el cliente manda en `session` (para el ledger de UX).
- No reemplaza el reporte del motor: para decisiones de ingeniería
  (frecuencias, mástiles) el dato es el Report; el asistente lo explica y
  sugiere, no lo decide por ti.
- El upstream es tuyo (local o pagado): este servicio no empaqueta un
  modelo; solo habla el formato.
