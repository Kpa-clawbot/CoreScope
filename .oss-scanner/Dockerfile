# OSS Scanner build image (https://github.com/anthropics/oss-scanner).
#
# The scanner builds this with the repository as the build context, then audits
# the finished image with no network. Everything a reproducer needs is fetched
# here: Go modules for all 16 modules, the npm packages for the frontend unit
# tests, and a warm build cache.
#
# This is not the release image (see /Dockerfile): it builds natively with the
# system C compiler instead of zig, because the scanner only needs a working
# host build, not static multi-arch binaries.
FROM golang:1.27-trixie

ENV DEBIAN_FRONTEND=noninteractive \
    CGO_ENABLED=1 \
    GOTOOLCHAIN=local \
    PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential ca-certificates git nodejs npm sqlite3 \
    && rm -rf /var/lib/apt/lists/*

COPY . /src
WORKDIR /src

# Same entry points the maintainers use: `make build` (cgo, sqlite build tags)
# and scripts/allmod.sh, which walks every go.mod. GO_LDFLAGS_OPTIMS is emptied
# so the binaries in dist/ keep their symbols and DWARF.
RUN bash scripts/allmod.sh mod download \
    && make build GO_LDFLAGS_OPTIMS= \
    && bash scripts/allmod.sh test -count=1 -run '^$' ./...

RUN npm ci --no-audit --no-fund

# Baseline tests. A failure is logged, not fatal, so the image stays usable.
RUN make test || echo 'WARNING: Go tests reported failures'
RUN sh test-all.sh || echo 'WARNING: frontend unit tests reported failures'

# Reproducers and patches must build without the network.
ENV GOPROXY=off \
    GOSUMDB=off

CMD ["bash"]
