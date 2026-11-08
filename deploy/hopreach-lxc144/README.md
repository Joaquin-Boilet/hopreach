# deploy/hopreach-lxc144 — la caja de publicación

Un LXC (VMID 144, `hopreach-dmr-web`, `172.26.10.244`) que publica la
solución completa del fork:

| Servicio | Puerto | Qué es |
|---|---|---|
| nginx | 80 | estático (`public/`) + proxies `/api/` (dmrsim) y `/v1/` (asistente) + `/dem-tiles/` |
| `dmrsim -serve` (systemd `dmrsim.service`) | 127.0.0.1:8090 | `/api/dmr/*`: topología con márgenes de terreno real, `simulate`, `policy` |
| asistente agéntico (`hopreach-assistant.service`) | 127.0.0.1:18096 | API compatible OpenAI (`/v1/chat/completions` con RAG + screenshots + tool loop) |
| Ollama | 127.0.0.1:11434 | upstream LLM del asistente — `qwen3-8t` (chat + tools) y `gemma4-8t` (visión), `num_thread=8` en el Modelfile; el RAG es lexical (embeds opcionales, `nomic-embed-text`) |

## Requisitos de la caja (elegidos por el perfil real de la solución)

- **8 vCPU / 16 GB RAM / 60 GB**: el peso real es el modelo de chat
  (~8,3 GB en disco, ~9 GB en RAM con KV) + el DEM cacheado (crece por
  zoom; ~2–6 GB por región) + Node + nginx. Sin GPU: el asistente local
  rinde para consular topología y lector de capturas.
- `nesting=1,keyctl=1` (Playwright de las capturas del asistente).

## Aprovisionado una vez (provisionado a mano, versionado aqui)

```bash
# dentro del LXC 144 (script en /root/p144u.sh, ya corrido):
#   nginx, node 22 (NodeSource), go 1.24, ollama
# modelo pull:
ollama pull bge-m3
ollama pull gemma4:12b
# par de modelos del asistente (threads por PARAMETER — el shim /v1 de
# ollama ignora options.num_thread del payload, medido 2026-11-08; sin
# esto el runner toma el cpuinfo del host — 40 threads sobre el cpuset
# de 8 — y ningún turno cierra):
printf 'FROM qwen3:8b\nPARAMETER num_thread 8\n'   > /tmp/mod-8t
ollama create qwen3-8t -f /tmp/mod-8t
printf 'FROM gemma4:12b\nPARAMETER num_thread 8\n' > /tmp/mod-8t
ollama create gemma4-8t -f /tmp/mod-8t
# deps del navegador del asistente (una vez):
sudo -u hopreach bash -c 'cd /opt/hopreach && npm i @playwright/test' \
  && sudo /opt/hopreach/node_modules/.bin/playwright install --with-deps chromium
```

## Flujo de deploy (repetible)

El workflow `.github/workflows/deploy.yml` (manual):
1. compila `dist/dmrsim` + `dist/hopreach-shareapi` en el runner (143),
2. arma bundle (`dist public examples tools/assistant deploy`),
3. lo envía por ssh/scp al 144 (necesita la llave del `runner@143`
   autorizada en el `runner@144`),
4. `install.sh` copia a `/opt/hopreach`, siembra config la primera vez
   (`config.yaml`, `dmr-network.yaml` — luego son del operador), restaura
   unidades nginx/systemd y reinicia servicios,
5. healthcheck: `curl /api/dmr/policy`.

Corrida a mano: `gh workflow run deploy-dmr -R Joaquin-Boilet/hopreach`.
