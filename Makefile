GO ?= go
PREFIX ?= /usr/local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/markos-narinin/git-snapshot/internal/app.Version=$(VERSION)
BINARY := git-snapshot

.PHONY: build test test-race vet check install clean release

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/git-snapshot

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: vet test test-race

install: build
	install -d $(DESTDIR)$(PREFIX)/bin $(DESTDIR)$(PREFIX)/share/man/man1 \
		$(DESTDIR)$(PREFIX)/share/bash-completion/completions \
		$(DESTDIR)$(PREFIX)/share/zsh/site-functions \
		$(DESTDIR)$(PREFIX)/share/fish/vendor_completions.d
	install -m 0755 bin/$(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	install -m 0644 man/git-snapshot.1 $(DESTDIR)$(PREFIX)/share/man/man1/git-snapshot.1
	install -m 0644 completions/git-snapshot.bash $(DESTDIR)$(PREFIX)/share/bash-completion/completions/git-snapshot
	install -m 0644 completions/_git-snapshot $(DESTDIR)$(PREFIX)/share/zsh/site-functions/_git-snapshot
	install -m 0644 completions/git-snapshot.fish $(DESTDIR)$(PREFIX)/share/fish/vendor_completions.d/git-snapshot.fish

clean:
	rm -rf bin dist

release: clean
	mkdir -p dist
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-$(VERSION)-darwin-amd64 ./cmd/git-snapshot
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-$(VERSION)-darwin-arm64 ./cmd/git-snapshot
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-$(VERSION)-linux-amd64 ./cmd/git-snapshot
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-$(VERSION)-linux-arm64 ./cmd/git-snapshot
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-$(VERSION)-windows-amd64.exe ./cmd/git-snapshot
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-$(VERSION)-windows-arm64.exe ./cmd/git-snapshot
	cd dist && if command -v sha256sum >/dev/null 2>&1; then sha256sum * > SHA256SUMS; else shasum -a 256 * > SHA256SUMS; fi
