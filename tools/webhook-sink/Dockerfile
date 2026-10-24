# テスト用の Webhook 受信コンテナ（本番のビルドには含めない）
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY main.go go.mod.sink ./
RUN mv go.mod.sink go.mod && CGO_ENABLED=0 go build -o /webhook-sink .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /webhook-sink /webhook-sink
EXPOSE 8080
ENTRYPOINT ["/webhook-sink"]
