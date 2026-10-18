# dkit web UI: Go binary + the tools the dkit CLI needs (it runs the CLI from /opt/dkit).
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /dkit-ui .

FROM docker:28-cli
RUN apk add --no-cache bash coreutils util-linux git openssh-client jq curl openssl restic gzip tzdata findutils
COPY --from=build /dkit-ui /usr/local/bin/dkit-ui
EXPOSE 9000
HEALTHCHECK --interval=30s --timeout=5s CMD wget -q -O /dev/null http://127.0.0.1:9000/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/dkit-ui"]
