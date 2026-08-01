# syntax=docker/dockerfile:1

# The Software Factory API is amd64-only because home-server, its sole deploy
# target, is x86. CI builds this image with --platform linux/amd64.
FROM golang:1.26.5-trixie AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api /usr/local/bin/api

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/api"]
