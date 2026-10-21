BIN      := plugins/longform-pipeline/scripts/longform
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X github.com/michaellady/longform-pipeline/internal/cli.Version=$(VERSION)

.PHONY: build test vet check install clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/longform

test:
	go test ./...

vet:
	go vet ./...

check: vet test build

clean:
	rm -f $(BIN)
