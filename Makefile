APP := ctxview
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PORT ?= 8787
LDFLAGS := -s -w -X main.version=$(VERSION)
PKG := .

.PHONY: build run test dist clean

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(APP) $(PKG)

run:
	go run -ldflags="-X main.version=$(VERSION)" $(PKG) -port $(PORT) -open

test:
	go test -v ./...

dist:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/$(APP)_$(VERSION)_darwin_arm64 $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/$(APP)_$(VERSION)_darwin_amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/$(APP)_$(VERSION)_linux_amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/$(APP)_$(VERSION)_linux_arm64 $(PKG)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/$(APP)_$(VERSION)_windows_amd64.exe $(PKG)
	@rm -f dist/checksums.txt
	@cd dist && (command -v sha256sum >/dev/null 2>&1 && sha256sum * > checksums.txt || shasum -a 256 * > checksums.txt)

clean:
	rm -rf $(APP) dist

