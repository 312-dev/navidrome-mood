# navidrome-mood
#
# A .ndp is just a ZIP of manifest.json + plugin.wasm. Packaging goes through
# python3 rather than zip(1) because zip is not installed everywhere, and a build
# that only works on machines with one extra tool is a bad default for a public
# plugin.

GO      ?= go
DIST    ?= dist
WASM    := $(DIST)/plugin.wasm
NDP     := $(DIST)/navidrome-mood.ndp
SOURCES := $(shell find . -name '*.go' -not -path './dist/*') plugin/manifest.json

# GOTOOLCHAIN=auto lets go fetch 1.25+ per-module, so the host toolchain version
# does not matter. Standard Go, not TinyGo: TinyGo builds smaller wasm but is not
# required, and standard Go gives the full stdlib.
export GOTOOLCHAIN = auto

.PHONY: all test vet fmt clean check

all: $(NDP)

$(WASM): $(SOURCES)
	@mkdir -p $(DIST)
	GOOS=wasip1 GOARCH=wasm $(GO) build -buildmode=c-shared -o $@ ./plugin

$(NDP): $(WASM) plugin/manifest.json
	@python3 -c "import zipfile,sys; \
z=zipfile.ZipFile('$@','w',zipfile.ZIP_DEFLATED); \
z.write('plugin/manifest.json','manifest.json'); \
z.write('$(WASM)','plugin.wasm'); \
z.close()"
	@echo "built $@ ($$(wc -c < $@) bytes)"

# check is what CI runs and what should pass before any commit.
check: fmt vet test

test:
	$(GO) test ./... -count=1

vet:
	$(GO) vet ./...

fmt:
	@test -z "$$($(GO) fmt ./...)" || (echo "gofmt made changes; commit them" && exit 1)

clean:
	rm -rf $(DIST)
