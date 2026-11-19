# Provimux image (PMX-10): upstream's Dockerfile with the ProvimuxVersion ldflag and
# the binary named provimux. Paths, port, working directory and config location are
# CLIProxyAPI's (/CLIProxyAPI/config.yaml, auth in /root/.cli-proxy-api), so a
# CLIProxyAPI compose file works unchanged; /CLIProxyAPI/CLIProxyAPI is kept as a
# link to the binary for commands that name it. Build from the repository root:
#   docker build -f .github/provimux/Dockerfile .
FROM golang:1.26-bookworm AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG PROVIMUX_VERSION=dev

RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}' -X 'github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo.ProvimuxVersion=${PROVIMUX_VERSION}'" -o ./provimux ./cmd/server/

FROM debian:bookworm

LABEL org.opencontainers.image.title="Provimux" \
      org.opencontainers.image.description="Provimux by providers.dev: one local endpoint for your AI subscriptions and API keys. A distribution of CLIProxyAPI." \
      org.opencontainers.image.vendor="providers.dev" \
      org.opencontainers.image.url="https://providers.dev" \
      org.opencontainers.image.source="https://github.com/ngvoicu/provimux" \
      org.opencontainers.image.licenses="MIT"

RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/*

RUN mkdir /CLIProxyAPI

COPY --from=builder ./app/provimux /CLIProxyAPI/provimux

RUN ln -s provimux /CLIProxyAPI/CLIProxyAPI

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./provimux"]
