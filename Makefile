.PHONY: run swagger sync-version version build-backend check-release-contract

CONFIG ?= $(CURDIR)/config.yaml

run:
	cd backend && GOCACHE=$(CURDIR)/.gocache go run ./cmd/grok2api --config "$(abspath $(CONFIG))" $(RUN_ARGS)

swagger:
	cd backend && GOCACHE=$(CURDIR)/.gocache go run github.com/swaggo/swag/cmd/swag@v1.16.6 init \
		-g main.go \
		-d cmd/grok2api,internal/transport/http \
		--parseInternal \
		--output docs \
		--outputTypes go,json,yaml

sync-version:
	bash scripts/sync-version.sh

version:
	@printf 'VERSION: '
	@cat VERSION
	@printf 'GHCR tag: '
	@bash scripts/image-tag.sh < VERSION

build-backend:
	cd backend && GOCACHE=$(CURDIR)/.gocache go build -o grok2api ./cmd/grok2api

check-release-contract:
	bash scripts/check-release-contract.sh
