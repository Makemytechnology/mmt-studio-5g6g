# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# MMT Studio Core (Go) — Top-level Makefile

SHELL     := /bin/bash
SCRIPT_DIR := $(shell pwd)
BINARY     := sacore-web
DPDK_DIR   := libs/dpdk-25.11
UPF_DIR    := nf/upf/dataplane

# Project version — derived from the most recent v<YYYY>.<MM>.<N>
# git tag (created by tools/release/publish.sh). Before the first
# publish of a month, falls back to <YYYY>.<MM>.0-dev so local
# tarball/deb/rpm/docker builds still get a reasonable label.
VERSION    := $(shell \
    v=$$(git describe --tags --abbrev=0 --match='v[0-9][0-9][0-9][0-9].[0-9][0-9].[0-9]*' 2>/dev/null); \
    if [ -n "$$v" ]; then echo "$${v#v}"; else echo "$$(date -u +%Y.%m).0-dev"; fi)

# Go build settings
export CGO_ENABLED := 1
export CGO_CFLAGS  := -I$(SCRIPT_DIR)/$(UPF_DIR)/include
export CGO_LDFLAGS := -L$(SCRIPT_DIR)/$(UPF_DIR) -L$(SCRIPT_DIR)/$(DPDK_DIR)/build/lib

.PHONY: all build dpdk upf test clean install run package \
        release-tarball deb release-deb release-rpm docker \
        publish publish-dry-run

all: build

# ── Build the Go binary ──
build: $(BINARY)

$(BINARY): $(shell find . -name '*.go' -not -path './codecs/*' 2>/dev/null)
	cd webservice && go build -o ../$(BINARY) ./cmd/sacore-web
	@echo "Built: $(BINARY) ($$(du -h $(BINARY) | cut -f1))"

# ── DPDK 25.11 ──
dpdk:
	@if [ ! -f $(DPDK_DIR)/build/lib/librte_hash.so ]; then \
		echo "Building DPDK 25.11..."; \
		cd $(DPDK_DIR) && meson setup build -Dmax_numa_nodes=1 \
			-Ddisable_drivers='*' -Denable_drivers='net/tap,net/ring,mempool/ring' \
			-Dtests=false -Dexamples='' && ninja -C build; \
	else \
		echo "DPDK already built."; \
	fi

# ── UPF dataplane ──
upf: dpdk
	@echo "Building UPF dataplane..."
	export LD_LIBRARY_PATH=$(SCRIPT_DIR)/$(DPDK_DIR)/build/lib:$$LD_LIBRARY_PATH && \
	cd $(UPF_DIR) && make

# ── Tests ──
test:
	@for m in oam db webservice infra nf libs/sacrypto services/ims; do \
		echo "Testing $$m..."; \
		(cd $$m && go test ./...) || exit 1; \
	done
	@echo "All tests passed."

# ── Clean ──
clean:
	rm -f $(BINARY)
	cd $(UPF_DIR) && make clean 2>/dev/null || true
	rm -rf $(DPDK_DIR)/build

# ── Install (build everything) ──
install:
	./install.sh

# ── Run ──
run: $(BINARY)
	./run.sh

# ── release-tarball ──
# Strip *_test.go + testdata/ via tools/release/strip-tests.sh, then
# tar.xz the cleaned tree. This is what the public release artefact
# looks like — tests stay in the repo for collaborators, but never
# end up in the downloadable tarball.
release-tarball:
	@echo "Building release tarball for v$(VERSION)..."
	@./tools/release/strip-tests.sh . release-tmp
	@mkdir -p dist
	@tar -cJf dist/mmt-studio-core-$(VERSION).tar.xz \
		--transform 's,^release-tmp,mmt-studio-core-$(VERSION),' \
		release-tmp
	@rm -rf release-tmp
	@echo "Tarball: dist/mmt-studio-core-$(VERSION).tar.xz ($$(du -h dist/mmt-studio-core-$(VERSION).tar.xz | cut -f1))"

# ── package (legacy alias for release-tarball) ──
package: release-tarball

# ── DEB package ──
# Produces dist/mmt-studio-core_<version>_<arch>.deb. Uses external
# template tools/release/deb/control.tmpl with __VERSION__ / __ARCH__
# substituted at build time, plus tools/release/deb/sacore.service as
# the systemd unit.
ARCH := $(shell dpkg --print-architecture 2>/dev/null || uname -m)

deb release-deb: build
	@echo "Creating .deb package for v$(VERSION) ($(ARCH))..."
	@rm -rf dist/deb
	@mkdir -p dist/deb/DEBIAN \
	          dist/deb/opt/mmt-studio-core \
	          dist/deb/opt/mmt-studio-core/webservice \
	          dist/deb/usr/bin \
	          dist/deb/etc/systemd/system
	@cp $(BINARY) dist/deb/opt/mmt-studio-core/
	@cp run.sh dist/deb/opt/mmt-studio-core/
	@cp -r webservice/templates dist/deb/opt/mmt-studio-core/webservice/
	@cp -r webservice/static    dist/deb/opt/mmt-studio-core/webservice/
	@ln -sf /opt/mmt-studio-core/sacore-web dist/deb/usr/bin/sacore-web
	@sed -e "s/__VERSION__/$(VERSION)/" -e "s/__ARCH__/$(ARCH)/" \
		tools/release/deb/control.tmpl > dist/deb/DEBIAN/control
	@cp tools/release/deb/sacore.service dist/deb/etc/systemd/system/
	@if command -v dpkg-deb >/dev/null 2>&1; then \
		dpkg-deb --build dist/deb dist/mmt-studio-core_$(VERSION)_$(ARCH).deb && \
		echo "DEB: dist/mmt-studio-core_$(VERSION)_$(ARCH).deb"; \
	else \
		echo "dpkg-deb not available — deb skeleton at dist/deb/"; \
	fi

# ── RPM package (via fpm — install with: gem install fpm) ──
# fpm converts the dist/deb skeleton into an RPM with the same payload.
release-rpm: deb
	@if ! command -v fpm >/dev/null 2>&1; then \
		echo "fpm not installed — skipping RPM. Install with 'gem install fpm'."; \
		exit 0; \
	fi
	@echo "Creating .rpm package for v$(VERSION)..."
	@cd dist && fpm -s deb -t rpm \
		-p mmt-studio-core-$(VERSION)-1.x86_64.rpm \
		mmt-studio-core_$(VERSION)_$(ARCH).deb
	@echo "RPM: dist/mmt-studio-core-$(VERSION)-1.x86_64.rpm"

# ── publish — mirror cleaned tree to the public-facing GitHub repo ──
# Refuses to publish if the working tree is dirty (so the public repo
# always reflects a real source commit). publish.sh auto-computes the
# next v<YYYY>.<MM>.<N> tag from the public repo's existing tags and
# pushes it alongside the snapshot commit. Use `make publish-dry-run`
# to preview without pushing.
publish:
	@./tools/release/publish.sh

publish-dry-run:
	@./tools/release/publish.sh --dry-run

# ── Docker image ──
# Build context: full repo (Dockerfile copies everything; install.sh
# inside the builder stage runs the same sweep as a local install).
# Image tagged both `mmt-studio-core:<version>` and `:latest`.
docker:
	@echo "Building Docker image mmt-studio-core:$(VERSION)..."
	@docker build \
		-t mmt-studio-core:$(VERSION) \
		-t mmt-studio-core:latest \
		-f tools/release/Dockerfile .
	@echo "Image: mmt-studio-core:$(VERSION)  (also tagged :latest)"
