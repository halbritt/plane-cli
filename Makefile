VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X plane-cli/internal/cli.Version=$(VERSION) -X plane-cli/internal/cli.Commit=$(COMMIT)
PREFIX  ?= $(HOME)/.local

.PHONY: build test check install smoke deploy dist clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o plane .

test:
	go test ./...

check:
	go vet ./...
	@fmt="$$(gofmt -l .)"; if [ -n "$$fmt" ]; then echo "gofmt needed: $$fmt"; exit 1; fi

install: build
	install -D -m 0755 plane $(PREFIX)/bin/plane

# Live happy-path run in a scratch project; needs PLANE_* in the environment.
smoke: build
	bash scripts/smoke.sh

# Install the binary AND generate ~/.config/plane-cli/ (config + key file)
# from an existing env file. See scripts/deploy.sh --help.
deploy:
	bash scripts/deploy.sh

# Release artifacts for the GitHub release workflow.
dist: test check
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/plane_linux_amd64/plane .
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/plane_linux_arm64/plane .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/plane_darwin_arm64/plane .
	cd dist && for d in plane_*; do tar -czf "$$d.tar.gz" -C "$$d" plane && rm -r "$$d"; done

clean:
	rm -f plane
	rm -rf dist
