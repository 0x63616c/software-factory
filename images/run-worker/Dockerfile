# syntax=docker/dockerfile:1

# The target per-Run pod (ADR-0011) contains a credentialed repository worker
# and a credential-free tools worker. They share only /work and poll distinct
# generation-specific Temporal queues. The main worker never reaches this pod
# through pods/exec.
#
# A shell and the repository toolchain are intentionally present because the
# agent runs project commands in the tools container. Projected credentials
# mount only into the repository container, whose activities expose fixed,
# typed operations rather than arbitrary model-selected argv.
#
# amd64 only. The home-server Talos node is the only deploy target and it is
# x86; CI builds --platform linux/amd64 on a native runner.

# ─────────────────────────────────────────────────────────────────────────────
# Stage 1: the embedded worker binary, and the Go toolchain the runtime stage
# borrows.
#
# The version comes from the image tag rather than a second declaration, and it
# is deliberately the same Go the worker is built with and the same one go.mod
# declares: a ticket that edits this module is tested inside this sandbox, so a
# toolchain skew here shows up as a test failure the reviewer cannot reproduce.
FROM golang:1.26.5-trixie AS build
WORKDIR /src

# The module has no vendor directory, so deps come from the proxy. Copied
# separately from the source so a source-only edit reuses the download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . ./
# The embedded worker binary this pod runs as its own Command (#434 step 3) —
# NOT cmd/worker, which is the main, control-plane worker's own binary and
# does not belong in this image. Same flags images/worker/Dockerfile builds
# cmd/worker with.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/run-worker ./cmd/run-worker \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/tool-worker ./cmd/tool-worker

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2: the per-Run worker runtime.
FROM debian:trixie-slim

# Downloaded tools are pinned by version AND checksum. A version tag alone is a
# moving target for a GitHub release asset, and this image executes them.
# bun matches the version CI and the rest of this repo build with
# (.github/workflows/ci.yml, oven-sh/setup-bun). A ticket's `bun install` must
# resolve the same lockfile the reviewer's machine does.
ARG BUN_VERSION=1.2.19
ARG BUN_SHA256=c3d3c14e9a5ec83ff67d0acfe76e4315ad06da9f34f59fc7b13813782caf1f66
# gh opens pull requests and performs repository operations. The launcher
# reads the projected token immediately before exec and exposes GH_TOKEN only
# to that short-lived gh process.
ARG GH_VERSION=2.96.0
ARG GH_SHA256=83d5c2ccad5498f58bf6368acb1ab32588cf43ab3a4b1c301bf36328b1c8bd60
# golangci-lint, pinned to what CI pins (.github/workflows/ci.yml:363,
# `version: v2.12.2`) for the same reason that comment gives: a rule that
# fires on one and not the other teaches people to distrust the wall.
ARG GOLANGCI_LINT_VERSION=2.12.2
ARG GOLANGCI_LINT_SHA256=8df580d2670fed8fa984aac0507099af8df275e665215f5c7a2ae3943893a553
# sqlc lets an agent regenerate committed Go query code after SQL changes.
ARG SQLC_VERSION=1.31.1
ARG SQLC_SHA256=497ae4fcdfa64c5b0c311ffe4c2bd991e43991e82e5367792ed78bc2dca27354
# Playwright must match the exact version in bun.lock. Its Chromium registry has
# three archives: Chrome for headed use, the headless shell Playwright launches
# by default, and ffmpeg for video support. Pin all three, rather than trusting
# a mutable browser CDN response at image build time.
ARG PLAYWRIGHT_VERSION=1.60.0
ARG PLAYWRIGHT_CHROMIUM_REVISION=1223
ARG PLAYWRIGHT_CHROMIUM_VERSION=148.0.7778.96
ARG PLAYWRIGHT_CHROMIUM_SHA256=e58a0612f6156ad287ff392c681f6e1154d0c938feccf631d0c5d7abf3bbf81a
ARG PLAYWRIGHT_CHROMIUM_HEADLESS_SHELL_SHA256=88817c574c1838a39f88fda0bbd043b4481fd385fb10e92c323e230844d636ce
ARG PLAYWRIGHT_FFMPEG_REVISION=1011
ARG PLAYWRIGHT_FFMPEG_SHA256=ebc74fc5b94830176a3c2914ae96bd8bc7f6a91f4f33890230f84a172ee61ccc

# GNU tar, coreutils (test, cat, printenv, rm) and a shell come from the base
# image. Added here: git, because repository lifecycle activities check the
# ticket's repository out and publish its branch, while `implement` edits and
# commits in that checkout; ca-certificates for the OpenAI and GitHub
# APIs; curl and unzip for the two downloads below; passwd for
# groupadd/useradd, which trixie-slim omits; gcc and libc6-dev so
# `CGO_ENABLED=1 go test -race` can link inside this image (#428); Node, because
# Vitest's forks pool uses it through Bun's `node:child_process` compatibility
# layer — all listed explicitly rather than relied on as a transitive pull-in.
# That could change under a base-image bump. Verified below by a CGO smoke
# build, not just `command -v gcc`, which would only prove the compiler is on
# PATH and not that it can actually link a cgo package.
#
# procps is not installed: its only use was the removed remote-process
# liveness probe. Sessions give the embedded worker a real process handle.
#
# The assertions below are the point of failing here rather than at 2am. One
# call per binary, because the multi-name form asserts less than it reads as:
# `RUN` execs `/bin/sh -c`, and in dash `command -v a b c` checks only its FIRST
# name (in bash it checks all and exits 0 if ANY resolves). Either way a single
# line naming four would assert one and imply four.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        gcc \
        git \
        libc6-dev \
        nodejs \
        passwd \
        unzip \
    && rm -rf /var/lib/apt/lists/* \
    && command -v tar >/dev/null \
    && command -v test >/dev/null \
    && command -v cat >/dev/null \
    && command -v gcc >/dev/null \
    && command -v git >/dev/null \
    && command -v node >/dev/null \
    && command -v printenv >/dev/null \
    && command -v rm >/dev/null

RUN curl -fsSL -o /tmp/gh.tar.gz \
        "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.tar.gz" \
    && echo "${GH_SHA256}  /tmp/gh.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/gh.tar.gz -C /tmp \
    && install -D -m 0755 "/tmp/gh_${GH_VERSION}_linux_amd64/bin/gh" /usr/local/libexec/gh-real \
    && rm -rf /tmp/gh.tar.gz "/tmp/gh_${GH_VERSION}_linux_amd64" \
    && /usr/local/libexec/gh-real --version

RUN curl -fsSL -o /tmp/bun.zip \
        "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-x64.zip" \
    && echo "${BUN_SHA256}  /tmp/bun.zip" | sha256sum -c - \
    && unzip -q /tmp/bun.zip -d /tmp \
    && install -m 0755 /tmp/bun-linux-x64/bun /usr/local/bin/bun \
    && ln -s bun /usr/local/bin/bunx \
    && rm -rf /tmp/bun.zip /tmp/bun-linux-x64 \
    && bun --version \
    && command -v bunx >/dev/null

RUN curl -fsSL -o /tmp/golangci-lint.tar.gz \
        "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64.tar.gz" \
    && echo "${GOLANGCI_LINT_SHA256}  /tmp/golangci-lint.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/golangci-lint.tar.gz -C /tmp \
    && install -m 0755 "/tmp/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64/golangci-lint" /usr/local/bin/golangci-lint \
    && rm -rf /tmp/golangci-lint.tar.gz "/tmp/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64" \
    && golangci-lint --version

RUN curl -fsSL -o /tmp/sqlc.tar.gz \
        "https://github.com/sqlc-dev/sqlc/releases/download/v${SQLC_VERSION}/sqlc_${SQLC_VERSION}_linux_amd64.tar.gz" \
    && echo "${SQLC_SHA256}  /tmp/sqlc.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/sqlc.tar.gz -C /tmp \
    && install -m 0755 /tmp/sqlc /usr/local/bin/sqlc \
    && rm -rf /tmp/sqlc.tar.gz /tmp/sqlc \
    && sqlc version

# Let Playwright's pinned dependency resolver, rather than a hand-maintained
# list of shared libraries, choose the Debian Trixie packages Chromium needs.
# The browser archives below are fetched explicitly so this executable payload
# remains checksum-pinned like every other downloaded tool in the image.
# `install-deps chromium` supplies Xvfb but not xauth, and `xvfb-run` shells out
# to xauth to mint the display's auth cookie. Without it the headed smoke check
# fails with `xvfb-run: error: xauth command not found`. Name it explicitly here
# rather than relying on the resolver's `tools` set, which does not carry it.
RUN bunx --yes "playwright@${PLAYWRIGHT_VERSION}" install-deps chromium \
    && apt-get update \
    && apt-get install -y --no-install-recommends xauth \
    && rm -rf /var/lib/apt/lists/* \
    && command -v xvfb-run >/dev/null \
    && command -v xauth >/dev/null \
    && rm -rf /root/.bun /root/.cache

RUN curl -fsSL -o /tmp/chrome-linux64.zip \
        "https://cdn.playwright.dev/builds/cft/${PLAYWRIGHT_CHROMIUM_VERSION}/linux64/chrome-linux64.zip" \
    && echo "${PLAYWRIGHT_CHROMIUM_SHA256}  /tmp/chrome-linux64.zip" | sha256sum -c - \
    && curl -fsSL -o /tmp/chrome-headless-shell-linux64.zip \
        "https://cdn.playwright.dev/builds/cft/${PLAYWRIGHT_CHROMIUM_VERSION}/linux64/chrome-headless-shell-linux64.zip" \
    && echo "${PLAYWRIGHT_CHROMIUM_HEADLESS_SHELL_SHA256}  /tmp/chrome-headless-shell-linux64.zip" | sha256sum -c - \
    && curl -fsSL -o /tmp/ffmpeg-linux.zip \
        "https://cdn.playwright.dev/dbazure/download/playwright/builds/ffmpeg/${PLAYWRIGHT_FFMPEG_REVISION}/ffmpeg-linux.zip" \
    && echo "${PLAYWRIGHT_FFMPEG_SHA256}  /tmp/ffmpeg-linux.zip" | sha256sum -c - \
    && install -d -m 0755 \
        "/ms-playwright/chromium-${PLAYWRIGHT_CHROMIUM_REVISION}" \
        "/ms-playwright/chromium_headless_shell-${PLAYWRIGHT_CHROMIUM_REVISION}" \
        "/ms-playwright/ffmpeg-${PLAYWRIGHT_FFMPEG_REVISION}" \
    && unzip -q /tmp/chrome-linux64.zip \
        -d "/ms-playwright/chromium-${PLAYWRIGHT_CHROMIUM_REVISION}" \
    && unzip -q /tmp/chrome-headless-shell-linux64.zip \
        -d "/ms-playwright/chromium_headless_shell-${PLAYWRIGHT_CHROMIUM_REVISION}" \
    && unzip -q /tmp/ffmpeg-linux.zip \
        -d "/ms-playwright/ffmpeg-${PLAYWRIGHT_FFMPEG_REVISION}" \
    && touch "/ms-playwright/chromium-${PLAYWRIGHT_CHROMIUM_REVISION}/INSTALLATION_COMPLETE" \
    && touch "/ms-playwright/chromium_headless_shell-${PLAYWRIGHT_CHROMIUM_REVISION}/INSTALLATION_COMPLETE" \
    && touch "/ms-playwright/ffmpeg-${PLAYWRIGHT_FFMPEG_REVISION}/INSTALLATION_COMPLETE" \
    && rm -f /tmp/chrome-linux64.zip /tmp/chrome-headless-shell-linux64.zip /tmp/ffmpeg-linux.zip

# The Go toolchain, taken from the build stage rather than downloaded again, so
# it cannot drift from the version this module declares.
COPY --from=build /usr/local/go /usr/local/go
# Prepended, not replaced: the base image's PATH is what puts tar, test and
# cat where the worker's argv expects them.
ENV PATH=/usr/local/go/bin:$PATH
ENV GOTOOLCHAIN=local

# Proves #428's actual claim — that a cgo package LINKS in this image — rather
# than just that `gcc` resolves on PATH. `command -v gcc` above would pass even
# if libc6-dev were missing and the link step failed; this would not.
RUN printf 'package main\nimport "C"\nfunc main() {}\n' > /tmp/cgo_check.go \
    && CGO_ENABLED=1 go build -o /tmp/cgo_check /tmp/cgo_check.go \
    && rm -f /tmp/cgo_check.go /tmp/cgo_check

# The pod's own Temporal worker: it polls this generation's private Session
# queue and is what CMD below actually runs. runWorkerBinaryPath in podspec.go
# is a contract with this exact install path.
COPY --from=build /out/run-worker /usr/local/bin/run-worker
COPY --from=build /out/tool-worker /usr/local/bin/tool-worker
COPY images/run-worker/bin/git-credential-projected /usr/local/bin/git-credential-projected
COPY images/run-worker/bin/gh /usr/local/bin/gh

# Git resolves this helper for every HTTPS credential request. Both launchers
# reopen the projected token file on every invocation, so kubelet Secret
# rotation takes effect without changing the long-lived Run Worker process.
RUN git config --system credential.helper /usr/local/bin/git-credential-projected

# uid/gid 1000, and it is a contract, not a convention: podspec.go pins
# runAsUser/runAsGroup/fsGroup to 1000 (runWorkerUID) so local file operations
# invariant holds — `test -e` exiting 1 must mean absent rather than unreadable,
# which is only true while one uid owns everything it writes under /work.
RUN groupadd --gid 1000 sandbox \
    && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash sandbox

# /work is an emptyDir at runtime, so this directory is MASKED the moment the
# pod starts and nothing baked under it survives. It exists for the `docker run`
# case and to document the mount point.
RUN install -d -o 1000 -g 1000 -m 0755 /work

ENV HOME=/home/sandbox
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

# WORKDIR is the working directory inherited by local typed tool executions
# because podspec.go sets no container WorkingDir.
#
# It is /work — the mount root — and DELIBERATELY NOT the repo checkout, which
# is the obvious-looking choice and is a trap. A WORKDIR the runtime has to
# create inside the emptyDir is created by the RUNTIME, as root, mode 0755:
#
#   $ docker run --tmpfs '/work:uid=0,gid=1000,mode=2775' <img with WORKDIR /work/repo> \
#       sh -c 'ls -ldn /work/repo; touch /work/repo/probe'
#   drwxr-sr-x 2 0 1000 ... /work/repo
#   touch: cannot touch '/work/repo/probe': Permission denied
#
# — so the worker uid could not write its own checkout, and every clone, build
# and test would fail as a permission error dressed up as a broken tool. /work
# itself is group-writable because the kubelet applies fsGroup 1000 to the
# emptyDir, and a directory the PROCESS creates under it is owned by the
# process. So the checkout is created by whatever clones it, at work.RepoDir,
# and repository tools resolve the checkout at work.RepoDir.
WORKDIR /work
USER 1000:1000

# Mirrors podspec.go's Command (runWorkerBinaryPath) so `docker run`
# behaves like the pod does. The pod sets its own, so this is documentation
# with a fallback, not a default the cluster relies on.
CMD ["/usr/local/bin/run-worker"]
