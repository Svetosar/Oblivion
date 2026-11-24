# SPDX-License-Identifier: AGPL-3.0-or-later
GO      ?= go
BIN     := bin/oblivion
IMAGE   ?= localhost/oblivion:dev
ENGINE  ?= podman

.PHONY: build test vet check run image image-save admin
# Build the admin SPA into internal/admin/dist (embedded into the binary).
admin:
	cd admin && npm ci && npm run build
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/oblivion
vet:
	$(GO) vet ./...
test:
	$(GO) test -race ./...
check: vet test
run: admin build
	OBLIVION_SECURE_COOKIE=false $(BIN)
image:
	$(ENGINE) build -f deploy/Containerfile -t $(IMAGE) .
image-save: image
	mkdir -p dist && $(ENGINE) save $(IMAGE) | gzip > dist/oblivion-image.tar.gz
