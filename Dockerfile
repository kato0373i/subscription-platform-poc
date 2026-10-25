# アプリのイメージ（api・worker-billing・worker-integration・worker-projection・migrate・devlogs）。
# ローカル・CI は TAGS=devtools（dev clock・/v1/dev/*・開発用 JWT を含む）。本番は TAGS を空にする。
FROM golang:1.26-alpine AS build
WORKDIR /src
ARG TAGS=""
ARG VERSION=dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags "${TAGS}" -ldflags "-X subscription-platform/internal/platform/config.Version=${VERSION}" -o /out/ ./cmd/api ./cmd/worker-billing ./cmd/worker-integration ./cmd/worker-projection ./cmd/migrate ./cmd/devlogs

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/ /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/api"]
