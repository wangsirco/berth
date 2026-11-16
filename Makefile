VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/wangsirco/berth/internal/version.Version=$(VERSION)
export CGO_ENABLED=0

.PHONY: test web build cross deploy
test:
	go vet ./... && go test ./...
web:
	cd web && npm ci && npm run build
build: web
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/berth-panel ./cmd/berth-panel
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/berth ./cmd/berth
cross: web
	for a in arm64 amd64; do \
	  GOOS=linux GOARCH=$$a go build -trimpath -ldflags '$(LDFLAGS)' -o bin/linux-$$a/berth-panel ./cmd/berth-panel; \
	  GOOS=linux GOARCH=$$a go build -trimpath -ldflags '$(LDFLAGS)' -o bin/linux-$$a/berth ./cmd/berth; \
	done
deploy: cross
	scripts/dev-deploy.sh
