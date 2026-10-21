# dkit web UI: Go binary + the tools the dkit CLI needs (it runs the CLI from /opt/dkit).
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY presets ./presets
COPY templates ./templates
COPY VERSION ./VERSION
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(cat VERSION)" -o /dkit-go ./cmd/dkit
WORKDIR /src/ui
COPY ui/go.mod ./
COPY ui/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /dkit-ui .

FROM scratch AS cli-export
COPY --from=build /dkit-go /dkit-go

FROM docker:28-cli
RUN apk add --no-cache bash coreutils util-linux git openssh-client jq curl openssl restic gzip tzdata findutils
COPY --from=build /dkit-ui /usr/local/bin/dkit-ui
COPY --from=build /dkit-go /usr/local/bin/dkit-go
EXPOSE 9000
HEALTHCHECK --interval=30s --timeout=5s CMD wget -q -O /dev/null http://127.0.0.1:9000/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/dkit-ui"]
