BIN      := plugins/longform-pipeline/scripts/longform
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X github.com/michaellady/longform-pipeline/internal/cli.Version=$(VERSION)
REPO     := michaellady/longform-pipeline
ARCH     := $(shell uname -m | sed s/x86_64/amd64/)
SETUP    := plugins/longform-pipeline/scripts/setup.sh
FIXTURE  := plugins/longform-pipeline/fixtures/sample-30s.mp4

.PHONY: build test vet check install clean launchd-install launchd-uninstall fixture release-dry release-preview

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/longform

test:
	go test ./...

vet:
	go vet ./...

check: vet test build

clean:
	rm -f $(BIN)
	rm -rf dist

# install fetches the latest darwin release for this machine's arch into $(BIN);
# with no release yet (or no gh), it builds from source instead.
install:
	@tag=$$(gh release view --repo $(REPO) --json tagName -q .tagName 2>/dev/null); \
	if [ -n "$$tag" ]; then \
	  echo "fetching $$tag darwin/$(ARCH)"; \
	  mkdir -p dist && gh release download "$$tag" --repo $(REPO) \
	    --pattern "longform_$${tag}_darwin_$(ARCH).tar.gz" --dir dist --clobber && \
	  tar -xzf "dist/longform_$${tag}_darwin_$(ARCH).tar.gz" -C $(dir $(BIN)) && \
	  chmod +x $(BIN); \
	else \
	  echo "no release found; building from source"; $(MAKE) build; \
	fi
	@$(BIN) version

launchd-install:
	bash $(SETUP) launchd-install

launchd-uninstall:
	bash $(SETUP) launchd-uninstall

fixture:
	bash $(SETUP) fixture $(FIXTURE)

# release-dry mirrors the build job of .github/workflows/release.yml locally:
# both darwin tarballs plus sha256 files into dist/, no tag, no upload. The
# version defaults to `git describe`; `make release-dry VERSION=v0.3.0` names
# the tarballs as the release would.
release-dry:
	rm -rf dist && mkdir -p dist
	for arch in arm64 amd64; do \
	  GOOS=darwin GOARCH=$$arch CGO_ENABLED=0 go build -trimpath \
	    -ldflags "-s -w $(LDFLAGS)" -o dist/longform ./cmd/longform && \
	  ( cd dist && tar -czf "longform_$(VERSION)_darwin_$$arch.tar.gz" longform && \
	    shasum -a 256 "longform_$(VERSION)_darwin_$$arch.tar.gz" > "longform_$(VERSION)_darwin_$$arch.tar.gz.sha256" && \
	    rm longform ); \
	done
	ls -la dist

# release-preview prints what the release workflow would decide for HEAD (run
# it on an up-to-date main): version, bump, title, summary, release or not,
# and why. Read-only: it runs git and `gh pr view`, nothing else.
release-preview:
	go run ./cmd/release-meta --repo $(REPO)
