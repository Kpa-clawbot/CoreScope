# corescope build entry point.
#
# Each Go module resolves internal dependencies through local replace directives.
# SQLite and PostgreSQL are supported by the same binaries. Native SQLite
# needs cgo; Linux cross-builds use zig cc with musl for fully static binaries.
#
# Quick reference:
#   make build                    # all four binaries for the host
#   make build-server             # just one
#   make crossbuild               # linux/amd64 + linux/arm64, static, into dist/
#   make test / vet / fmt-check   # across all modules
#   make docker-build             # multi-arch image via buildx

GO                ?= $(shell command -v go)
GIT_VERSION       ?= $(shell git describe --tags --match "v*" 2>/dev/null || echo unknown)
GIT_COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GOENV_GOOS        := $(shell "$(GO)" env GOOS)
GOENV_GOARCH      := $(shell "$(GO)" env GOARCH)
GOOS              ?= $(GOENV_GOOS)
GOARCH            ?= $(GOENV_GOARCH)

CGO_server       := 1
CGO_ingestor     := 1
CGO_decrypt      := 1
CGO_migrate      := 1
GO_BUILD_TAGS    ?= netgo,osusergo,sqlite_omit_load_extension
GO_BUILD_FLAGS    ?= -trimpath
GO_LDFLAGS_OPTIMS ?= -s -w

CMDS              := server ingestor decrypt migrate
DIST              := dist

# Per-binary version stamping. Only these two read build metadata; ingestor and
# migrate take none. Kept as make variables so crossbuild and the host build
# cannot drift apart.
LDFLAGS_server    := -X main.Version=$(GIT_VERSION) -X main.Commit=$(GIT_COMMIT) -X main.BuildTime=$(BUILD_TIME)
LDFLAGS_ingestor  :=
LDFLAGS_decrypt   := -X main.version=$(GIT_VERSION)
LDFLAGS_migrate   :=

# zig target triples for the platforms we ship. musl, so the result is static.
ZIG_TARGET_linux_amd64 := x86_64-linux-musl
ZIG_TARGET_linux_arm64 := aarch64-linux-musl
comma             := ,
CROSS_PLATFORMS   := linux/amd64 linux/arm64

DOCKER_IMAGE      ?= ghcr.io/kpa-clawbot/corescope
DOCKER_TAG        ?= $(GIT_VERSION)
DOCKER_PLATFORMS  ?= linux/amd64,linux/arm64

.PHONY: all build crossbuild test vet fmt-check tidy clean docker-build docker-push help

all: build

help:
	@echo "targets: build crossbuild test vet fmt-check tidy clean docker-build docker-push"
	@echo "         build-{$(shell echo $(CMDS) | tr ' ' ',')}"

# -- host builds ---------------------------------------------------------------

build: $(addprefix build-,$(CMDS))

build-%:
	@mkdir -p $(DIST)
	cd cmd/$* && CGO_ENABLED=$(CGO_$*) GOOS=$(GOOS) GOARCH=$(GOARCH) "$(GO)" build \
		-tags $(GO_BUILD_TAGS) $(GO_BUILD_FLAGS) \
		-ldflags "$(GO_LDFLAGS_OPTIMS) $(LDFLAGS_$*)" \
		-o ../../$(DIST)/corescope-$* .

# -- cross builds --------------------------------------------------------------
#
# The host build above deliberately leaves CC alone: native builds, `go vet` and
# `-race` must use the host compiler. Only these recipes hand the build to zig.

crossbuild: $(foreach p,$(CROSS_PLATFORMS),$(foreach c,$(CMDS),crossbuild-$(c)-$(subst /,-,$(p))))

define CROSSBUILD_RULE
.PHONY: crossbuild-$(2)-$(3)-$(4)
crossbuild-$(2)-$(3)-$(4):
	@mkdir -p $$(DIST)
	@command -v zig >/dev/null || { echo "zig not found: needed to cross-compile native SQLite. See https://ziglang.org/download/"; exit 1; }
	cd cmd/$(2) && CGO_ENABLED=$$(CGO_$(2)) GOOS=$(3) GOARCH=$(4) CC="zig cc -target $(1)" \
		"$$(GO)" build -tags $$(GO_BUILD_TAGS) $$(GO_BUILD_FLAGS) \
			-ldflags '$$(GO_LDFLAGS_OPTIMS) -extldflags "-static -Wl$(comma)-s" $$(LDFLAGS_$(2))' \
			-o ../../$$(DIST)/corescope-$(2)-$(3)-$(4) .
endef

$(foreach c,$(CMDS),\
  $(eval $(call CROSSBUILD_RULE,$(ZIG_TARGET_linux_amd64),$(c),linux,amd64))\
  $(eval $(call CROSSBUILD_RULE,$(ZIG_TARGET_linux_arm64),$(c),linux,arm64)))

# -- checks --------------------------------------------------------------------

test:
	CGO_ENABLED=1 bash scripts/allmod.sh test ./...

vet:
	bash scripts/allmod.sh vet ./...

fmt-check:
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then echo "gofmt required on:"; echo "$$unformatted"; exit 1; fi; \
	echo "gofmt: clean"

tidy:
	bash scripts/allmod.sh mod tidy

clean:
	rm -rf $(DIST)

# -- docker --------------------------------------------------------------------

docker-build:
	docker buildx build . \
		--platform=$(DOCKER_PLATFORMS) \
		--build-arg=APP_VERSION=$(GIT_VERSION) \
		--build-arg=GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg=BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-push: DOCKER_PUSH_FLAG := --push
docker-push:
	docker buildx build . \
		--platform=$(DOCKER_PLATFORMS) \
		--build-arg=APP_VERSION=$(GIT_VERSION) \
		--build-arg=GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg=BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG) $(DOCKER_PUSH_FLAG)
