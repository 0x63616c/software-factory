# syntax=docker/dockerfile:1

# The software-factory Temporal worker (ADR-0011): the process that dispatches
# factory Tickets, owns each stage's AgentWorkflow child, calls the model and
# persists reference-backed transcripts. Per-ticket Run Workers execute
# only the typed tool activity. This is the first pod in the cluster with the
# provider credential and the Kubernetes grant used to create those Run Workers.
#
# `cmd/worker` landed (D1, #340) and CI builds this image on every change to it
# — the `build-software-factory-worker` job. The note that used to sit here,
# saying the composition root did not exist yet and CI skipped this file, was
# true only until D1 landed.
#
# amd64 only. The home-server Talos node is the only deploy target and it is
# x86; CI builds --platform linux/amd64 on a native runner.

FROM golang:1.26.5-trixie AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# distroless static: no shell, no package manager, nothing to exec into. That is
# worth more here than in any other image in this repo — this is the only
# workload holding a ServiceAccount token, and the blast radius of a foothold in
# it is the Run Worker namespace's whole RBAC grant.
#
# It ships ca-certificates (GitHub, the OpenAI auth endpoint) and /etc/passwd
# entries for uid 65532. NO tzdata, deliberately: A3 (#331) renders every
# timestamp as RFC3339 UTC, and time.Local is banned by .golangci.yml, so a
# missing zoneinfo database cannot change a rendered string.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/worker /usr/local/bin/worker

# 65532 is distroless's `nonroot`. Agent artifacts go through the blob service,
# so this process needs no writable repository or transcript volume.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/worker"]
