#!/bin/bash
# Instalador del despliegue DMR en el LXC 144 (hopreach-dmr-web).
#
# Lo ejecuta el workflow de deploy (desde el runner 143, por ssh) tras
# descargar el bundle. Es idempotente: actualiza lo versionado (binarios,
# public/, ejemplos, asistente), NO sobreescribe lo del operador
# (config.yaml, dmr-network.yaml ya editados), y reinicia los servicios.
#
# Bundle esperado en /opt/hopreach.incoming/... descomprimido ya en
# /usr/local/src/hopreach-bundle por el deploy.
set -euo pipefail

BUNDLE=/usr/local/src/hopreach-bundle
TARGET=/opt/hopreach

[ -d "$BUNDLE" ] || { echo "install: falta $BUNDLE (el deploy descomprime ahí)"; exit 1; }

echo "install: usuarios/permisos"
useradd -m -s /bin/bash hopreach 2>/dev/null || true
# sudo passwordless — mismo patrón de la casa (deps de playwright del
# asistente se instalan una vez por el propio usuario).
echo 'hopreach ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/hopreach
chmod 440 /etc/sudoers.d/hopreach

echo "install: copiando artifacts"
mkdir -p "$TARGET"
cp -a "$BUNDLE/dist"          "$TARGET/dist"
cp -a "$BUNDLE/public/."      "$TARGET/public/"
mkdir -p "$TARGET/examples"
cp -a "$BUNDLE/examples/."    "$TARGET/examples/" 2>/dev/null || true
mkdir -p "$TARGET/tools"
cp -a "$BUNDLE/tools/."       "$TARGET/tools/"
chown -R hopreach:hopreach    "$TARGET"

echo "install: seed de configuración (idempotente)"
if [ ! -f "$TARGET/dmr-network.yaml" ]; then
  cp "$TARGET/examples/dmr/dmr-network.example.yaml" "$TARGET/dmr-network.yaml"
  echo "install: dmr-network.yaml sembrado desde el ejemplo Córdillera — EDÍTALO"
fi
if [ ! -f "$TARGET/config.yaml" ]; then
  sed -e "s#network_path: dmr-network.yaml#network_path: /opt/hopreach/dmr-network.yaml#" \
      -e "s#dem_cache_dir: dem-cache#dem_cache_dir: /opt/hopreach/dem-cache#" \
      -e "s#boundary_path: examples/dmr/boundary-cordillera-rm.geojson#boundary_path: /opt/hopreach/examples/dmr/boundary-cordillera-rm.geojson#" \
      -e "s#store_dir: /tmp/plans#store_dir: /opt/hopreach/share-store#" \
      -e "s#listen_addr: 127.0.0.1:8081#listen_addr: 127.0.0.1:8081#" \
      "$TARGET/examples/dmr/config.example-dmr.yaml" > "$TARGET/config.yaml"
  echo "install: config.yaml sembrado desde el ejemplo — EDÍTALO"
fi
mkdir -p "$TARGET/dem-cache" "$TARGET/share-store"
chown -R hopreach:hopreach "$TARGET"

echo "install: deps del asistente (npm + chromium playwright; la primera vez)"
if [ ! -d "$TARGET/node_modules" ]; then
  cp "$BUNDLE/package.json" "$BUNDLE/package-lock.json" "$TARGET/" 2>/dev/null || true
  (cd "$TARGET" && npm ci --no-audit --no-fund 2>&1 | tail -1)
  # deps del sistema las pone root; el NAVEGADOR va a un cache COMPARTIDO
  # (default sería $HOME del que instala — y el servicio corre como
  # hopreach, no root).
  (cd "$TARGET" && npx playwright install-deps chromium 2>&1 | tail -1) || true
  (cd "$TARGET" && sudo -u hopreach env \
    PLAYWRIGHT_BROWSERS_PATH=/opt/hopreach/pw-cache \
    npx playwright install chromium 2>&1 | tail -1) || true
  chown -R hopreach:hopreach "$TARGET"
fi

echo "install: unidades systemd"
cp "$BUNDLE/deploy/hopreach-lxc144/dmrsim.service"           /etc/systemd/system/
cp "$BUNDLE/deploy/hopreach-lxc144/hopreach-assistant.service" /etc/systemd/system/
systemctl daemon-reload

echo "install: nginx"
cp "$BUNDLE/deploy/hopreach-lxc144/nginx-site.conf" /etc/nginx/sites-available/hopreach-dmr.conf
ln -sf /etc/nginx/sites-available/hopreach-dmr.conf /etc/nginx/sites-enabled/hopreach-dmr.conf
rm -f /etc/nginx/sites-enabled/default
nginx -t

echo "install: arrancando servicios"
systemctl restart dmrsim.service
systemctl restart hopreach-assistant.service 2>/dev/null || true
systemctl reload nginx

sleep 2
echo "install: healthchecks"
curl -fsS --max-time 10 http://127.0.0.1:8090/api/dmr/policy > /dev/null && echo "  dmrsim api ✓"
curl -fsS --max-time 10 http://127.0.0.1/v1/models?x=1  > /dev/null 2>&1 && echo "  asistente vía nginx ✓" || echo "  asistente: revisa hopreach-assistant.service (upstream ollama puede seguir cargando)"
echo "install: ok"

# El índice RAG va AL FINAL y NON-FATAL: el deploy termina arriba aunque
# el embedding remoto se demore (ollama carga bge-m3 frío la primera vez);
# se reintentará en el próximo deploy o a mano:
#   cd /opt/hopreach && sudo -u hopreach node tools/assistant/index.js
echo "install: índice RAG (se genera en destino contra ollama si falta)"
if [ ! -s "$TARGET/tools/assistant/data/docs-index.json" ]; then
  (
    cd "$TARGET"
    sudo -u hopreach env \
      PATH=/usr/local/bin:/usr/bin:/bin \
      ASSISTANT_BASE_URL=http://127.0.0.1:11434/v1 \
      ASSISTANT_EMBED_MODEL=bge-m3 \
      node tools/assistant/index.js 2>&1 | tail -2
  ) || echo "  RAG pendiente: reintentar con 'node tools/assistant/index.js' en destino (no bloquea el despliegue)"
fi
