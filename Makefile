SHELL := /bin/sh

GIT_COMMIT := $(shell git rev-parse --verify HEAD)
# GIT_DIRTY is the first Git fact captured, before any generation runs.
GIT_DIRTY := $(shell test -n "$$(git status --porcelain --untracked-files=all)" && printf '%s' -dirty)
IMAGE_REF ?= ghcr.io/deadfin-org/deadfin
IMAGE_TAG ?= $(GIT_COMMIT)$(GIT_DIRTY)
PLATFORM ?= linux/amd64
REVISION ?= $(GIT_COMMIT)
# Build identity: tools/image-publication
# is the single ref-to-channel/version mapping. Any other ref is local.
BUILD_REF := $(shell git symbolic-ref --short HEAD 2>/dev/null)
PUBLICATION := $(shell tools/image-publication "$(BUILD_REF)" 2>/dev/null)
CHANNEL ?= $(if $(PUBLICATION),$(patsubst channel=%,%,$(filter channel=%,$(PUBLICATION))),local)
VERSION ?= $(if $(PUBLICATION),$(patsubst version=%,%,$(filter version=%,$(PUBLICATION))),0.0.0-local)
DIRTY ?= $(GIT_DIRTY)
override IMAGE_REFERENCE := $(IMAGE_REF):$(IMAGE_TAG)
SOURCE_DATE_EPOCH ?= $(shell git show -s --format=%ct HEAD)
ARCHIVE ?= dist/deadfin-$(IMAGE_TAG)-$(subst /,-,$(PLATFORM)).tar
IMAGE_LOAD_ARCHIVE ?= dist/.deadfin-image-$(IMAGE_TAG)-$(subst /,-,$(PLATFORM)).tar
BUILDX ?= docker buildx build
BUILD_ARGS = --build-arg REVISION="$(REVISION)" --build-arg VERSION="$(VERSION)" --build-arg CHANNEL="$(CHANNEL)" --build-arg DIRTY="$(DIRTY)" --build-arg SOURCE_DATE_EPOCH="$(SOURCE_DATE_EPOCH)"

.PHONY: build build-identity test skia image image-archive image-verify image-push ensure-clean upstream

# Validation gates (difftool, goldenrec, ledger, convention gates).
include validation.mk

# upstream fetches the pinned Jellyfin source tree into upstream/jellyfin at
# the revision recorded in upstream/PIN. The tree is the behavior oracle for
# the source-attestation tests and is never committed. Re-run after a PIN
# change; the target refuses to move a tree that has local changes.
upstream:
	@pin=$$(cat upstream/PIN); \
	if [ -d upstream/jellyfin/.git ]; then \
		test -z "$$(git -C upstream/jellyfin status --porcelain)" || { echo "upstream/jellyfin has local changes; refusing to move it"; exit 1; }; \
		git -C upstream/jellyfin fetch -q origin "$$pin" 2>/dev/null || git -C upstream/jellyfin fetch -q origin; \
	else \
		git clone -q --no-checkout https://github.com/jellyfin/jellyfin.git upstream/jellyfin; \
	fi; \
	git -C upstream/jellyfin checkout -q --detach "$$pin"; \
	cp ledger/inputs/jellyfin-openapi-stable.json upstream/jellyfin-openapi-stable.json; \
	echo "upstream/jellyfin at $$(git -C upstream/jellyfin rev-parse HEAD)"

build:
	go build ./...

# build-identity stages the discovered build identity out of tree and builds
# ./deadfin with the overlay. The tree is never modified. Plain `build`
# stays the checked-in local/unknown/dirty identity.
build-identity:
	staging=$$(mktemp -d) && go run ./tools/buildinfo-gen -o "$$staging" && go build -overlay "$$staging/overlay.json" -o deadfin ./cmd/deadfin

# test runs the suite with a private TMPDIR that is removed afterwards, and
# kills processes the tests leave behind (tools/ci/subreaper).
test:
	go run ./tools/ci/subreaper go test ./...

# skia installs the pinned SkiaSharp native library at the fixed dlopen path
# that the Linux image tests use. tools/skia-fetch downloads the NuGet package
# only on a cache miss and verifies the package and the library by SHA-256.
SKIA_LIB ?= /usr/local/lib/deadfin/libSkiaSharp.so
SUDO ?= sudo
skia:
	go run ./tools/skia-fetch -o dist/skia/libSkiaSharp.so
	$(SUDO) install -D -m 0755 dist/skia/libSkiaSharp.so "$(SKIA_LIB)"

image:
	@mkdir -p "$(dir $(IMAGE_LOAD_ARCHIVE))"
	@printf 'Image reference: %s\n' "$(IMAGE_REFERENCE)"
	$(BUILDX) --platform "$(PLATFORM)" $(BUILD_ARGS) --tag "$(IMAGE_REFERENCE)" --output "type=docker,dest=$(IMAGE_LOAD_ARCHIVE),rewrite-timestamp=true" .
	docker image load --input "$(IMAGE_LOAD_ARCHIVE)"
	@rm -f "$(IMAGE_LOAD_ARCHIVE)"

image-archive: image-verify
	@mkdir -p "$(dir $(ARCHIVE))"
	@printf 'Image reference: %s\n' "$(IMAGE_REFERENCE)"
	@printf 'Image archive: %s\n' "$(ARCHIVE)"
	docker image save --output "$(ARCHIVE)" "$(IMAGE_REFERENCE)"

image-verify: image
	./tools/verify-image.sh "$(IMAGE_REFERENCE)" "$(PLATFORM)"

ensure-clean:
	@test -z "$$(git status --porcelain --untracked-files=all)" || { printf '%s\n' 'Refusing to push an image built from a dirty worktree.' >&2; exit 1; }

image-push: ensure-clean image-verify
	@printf 'Image reference: %s\n' "$(IMAGE_REFERENCE)"
	docker push "$(IMAGE_REFERENCE)"
