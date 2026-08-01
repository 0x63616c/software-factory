# syntax=docker/dockerfile:1

# The stateless webhook relay is deliberately its own binary and image: it is
# a platform edge service, not a software-factory workflow component.
FROM golang:1.26.5-trixie AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/relay /usr/local/bin/relay

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/relay"]
