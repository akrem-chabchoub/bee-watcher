.PHONY: all build go web test test-web fuzz sim dev clean

BIN := dist/bee-scope

all: build

# Full build: UI first (it is embedded into the binary), then Go.
build: web go

go:
	go build -o $(BIN) ./cmd/bee-scope

web:
	cd web && npm ci && npm run build

test:
	go vet ./...
	go test -race ./...

test-web:
	cd web && npm run typecheck

fuzz:
	go test ./pkg/source/logs -run XXX -fuzz FuzzParse -fuzztime 30s
	go test ./pkg/source/otlp -run XXX -fuzz FuzzDecode -fuzztime 30s

# Simulated cluster, no Kubernetes needed.
sim: build
	./$(BIN) --sim

# UI development: Vite with hot reload on :5173 proxies /api to bee-scope on :7070.
dev:
	@echo "run in two terminals:"
	@echo "  go run ./cmd/bee-scope --sim     (or without --sim against k3d-bee)"
	@echo "  cd web && npm run dev            (then open http://127.0.0.1:5173)"

clean:
	rm -rf dist
