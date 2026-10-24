.PHONY: all build go web test test-web fuzz sim dev clean app install-app

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

# macOS app: double-click to start bee-scope and open the UI; no terminal
# needed. It runs in the background (no Dock icon); quit it from the UI.
APP := dist/BeeScope.app
app: build
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS
	cp $(BIN) $(APP)/Contents/MacOS/bee-scope
	printf '%s\n' '#!/bin/bash' \
		'mkdir -p "$$HOME/Library/Logs"' \
		'exec "$$(dirname "$$0")/bee-scope" --open "$$@" >>"$$HOME/Library/Logs/bee-scope.log" 2>&1' \
		> $(APP)/Contents/MacOS/BeeScope
	chmod +x $(APP)/Contents/MacOS/BeeScope
	printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?>' \
		'<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
		'<plist version="1.0"><dict>' \
		'<key>CFBundleName</key><string>BeeScope</string>' \
		'<key>CFBundleIdentifier</key><string>org.ethswarm.bee-scope</string>' \
		'<key>CFBundleExecutable</key><string>BeeScope</string>' \
		'<key>CFBundlePackageType</key><string>APPL</string>' \
		'<key>CFBundleVersion</key><string>1</string>' \
		'<key>LSUIElement</key><true/>' \
		'</dict></plist>' > $(APP)/Contents/Info.plist
	@echo "built $(APP): double-click it (or: open $(APP))"

install-app: app
	mkdir -p $(HOME)/Applications
	rm -rf $(HOME)/Applications/BeeScope.app
	cp -R $(APP) $(HOME)/Applications/
	@echo "installed ~/Applications/BeeScope.app (find it in Launchpad / Spotlight: BeeScope)"

clean:
	rm -rf dist
