GO       ?= go
BINARY   := git-by
BINDIR   := bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: all build install test vet clean

all: build

build:
	@mkdir -p $(BINDIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(BINARY) ./cmd/git-by

install:
	CGO_ENABLED=0 $(GO) install -trimpath -ldflags "$(LDFLAGS)" ./cmd/git-by

test:
	$(GO) test -v -race ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf $(BINDIR) dist $(BINARY) $(BINARY).exe coverage.out coverage.txt
