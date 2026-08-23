SHELL := /bin/bash

GIT_SHORT_VERSION ?= $(shell git describe --tags --abbrev=8 --always)
GIT_LONG_VERSION ?= $(shell git describe --tags --abbrev=8 --dirty --always --long)
LDFLAGS ?= -w -s \
	-X 'github.com/xolo-gateway/xolo/internal/build.ShortVersion=$(GIT_SHORT_VERSION)' \
	-X 'github.com/xolo-gateway/xolo/internal/build.LongVersion=$(GIT_LONG_VERSION)'

GCFLAGS ?= -trimpath=$(PWD)
ASMFLAGS ?= -trimpath=$(PWD) \

CI_EVENT ?= push

# Pinned so `make generate` is reproducible: an unpinned `npx @tailwindcss/cli`
# resolves to whatever is latest on npm, which makes the `generated` CI job
# fail on a Tailwind release rather than on an actual stale file. Keep in sync
# with the `tailwindcss` version in package.json.
TAILWIND_VERSION ?= 4.2.1

RELEASE_CHANNEL ?= $(shell git rev-parse --abbrev-ref HEAD)
COMMIT_TIMESTAMP = $(shell git show -s --format=%ct)
RELEASE_VERSION ?= $(shell TZ=Europe/Paris date -d "@$(COMMIT_TIMESTAMP)" +%Y.%-m.%-d)-$(RELEASE_CHANNEL).$(shell date -d "@${COMMIT_TIMESTAMP}" +%-H%M).$(shell git rev-parse --short HEAD)

GORELEASER_ARGS ?= release --snapshot --clean

watch: .env tools/modd/bin/modd
	tools/modd/bin/modd

run-with-env: .env
	( set -o allexport && source .env && set +o allexport && $(value CMD))

build: build-server build-frontend all-plugins

all-plugins: cleanup-plugins $(foreach plugin,$(shell find ./plugins/ -mindepth 1  -maxdepth 1 -type d -printf '%f\n'), plugin-$(plugin))

cleanup-plugins:
	rm -rf bin/plugins/*

plugin-%:
	CGO_ENABLED=0 \
		go build \
			-ldflags "$(LDFLAGS)" \
			-gcflags "$(GCFLAGS)" \
			-asmflags "$(ASMFLAGS)" \
			-o bin/plugins/$* \
			./plugins/$*/

build-%: generate
	CGO_ENABLED=0 \
		go build \
			-ldflags "$(LDFLAGS)" \
			-gcflags "$(GCFLAGS)" \
			-asmflags "$(ASMFLAGS)" \
			-o ./bin/$* ./cmd/$*

purge:
	rm -rf *.sqlite* index.bleve

.PHONY: test
test:
	go test ./...

# Runs the store suite against both backends. PostgreSQL is provided by a
# throwaway testcontainers instance, so a running Docker daemon is required.
# Set XOLO_TEST_POSTGRES_DSN to target an existing server instead.
.PHONY: test-integration
test-integration:
	go test -tags integration -timeout 15m ./internal/adapter/gorm/...

# Generates a SQLite database populated with coherent fake data for E2E tests.
# See cmd/seed/README.md for the catalog of the generated fixture.
SEED_DSN ?= e2e.sqlite
SEED_DAYS ?= 90
.PHONY: seed
seed:
	go run ./cmd/seed -dsn $(SEED_DSN) -days $(SEED_DAYS) -force

build-frontend:
	cd frontend && npm ci && npm run build

generate: tools/templ/bin/templ
	tools/templ/bin/templ generate
	npx @tailwindcss/cli@$(TAILWIND_VERSION) -i misc/tailwind/templui.css -o internal/http/handler/webui/common/assets/templui.css

bin/templ: tools/templ/bin/templ
	mkdir -p bin
	ln -fs $(PWD)/tools/templ/bin/templ bin/templ

tools/templ/bin/templ:
	mkdir -p tools/templ/bin
	GOBIN=$(PWD)/tools/templ/bin go install github.com/a-h/templ/cmd/templ@v0.3.1001

tools/modd/bin/modd:
	mkdir -p tools/modd/bin
	GOBIN=$(PWD)/tools/modd/bin go install github.com/cortesi/modd/cmd/modd@latest

tools/act/bin/act:
	mkdir -p tools/act/bin
	cd tools/act && curl https://raw.githubusercontent.com/nektos/act/master/install.sh | bash -

ci: tools/act/bin/act
	tools/act/bin/act $(CI_EVENT)

tools/goreleaser/bin/goreleaser:
	mkdir -p tools/goreleaser/bin
	GOBIN=$(PWD)/tools/goreleaser/bin go install github.com/goreleaser/goreleaser/v2@latest

tools/templui/bin/templui:
	mkdir -p tools/templui/bin
	GOBIN=$(PWD)/tools/templui/bin go install github.com/templui/templui/cmd/templui@latest

goreleaser: tools/goreleaser/bin/goreleaser
	REPO_OWNER=$(shell whoami) tools/goreleaser/bin/goreleaser $(GORELEASER_ARGS)

.env:
	cp .env.dist .env

tools/protoc-gen-go/bin/protoc-gen-go:
	mkdir -p tools/protoc-gen-go/bin
	GOBIN=$(PWD)/tools/protoc-gen-go/bin go install google.golang.org/protobuf/cmd/protoc-gen-go@latest

tools/protoc-gen-go-grpc/bin/protoc-gen-go-grpc:
	mkdir -p tools/protoc-gen-go-grpc/bin
	GOBIN=$(PWD)/tools/protoc-gen-go-grpc/bin go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

generate-proto: tools/protoc-gen-go/bin/protoc-gen-go tools/protoc-gen-go-grpc/bin/protoc-gen-go-grpc
	PATH=$(PWD)/tools/protoc-gen-go/bin:$(PWD)/tools/protoc-gen-go-grpc/bin:$(PATH) \
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/pluginsdk/proto/plugin.proto

include misc/*/*.mk

