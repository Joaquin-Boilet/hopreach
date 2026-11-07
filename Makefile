.PHONY: wasm dmrsim dmr-test

# Builds the browser-side WASM module (see wasm/main.go) and copies the Go
# toolchain's own wasm_exec.js runtime shim alongside it - both are
# generated artifacts (not committed; see .gitignore) needed for local
# development without Docker. The Docker image builds these itself as part
# of its normal build stage instead of using this target.
wasm:
	GOOS=js GOARCH=wasm go build -o public/hopreach.wasm ./wasm
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" public/wasm_exec.js

# DMR mode (Motorola TRBO fork layer): run the sample DMR server against
# the bundled example topology (see docs/DMR_MODE.md).
dmrsim:
	go run ./cmd/dmrsim -network examples/dmr/dmr-network.example.yaml -serve :8090

# Quick DMR sanity check: parse the example topology + run the sample
# scenario headless against a flat grid (no DEM needed).
dmr-test:
	go test ./internal/dmr ./internal/dmrsim
