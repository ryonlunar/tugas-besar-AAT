FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY contracts ./contracts
COPY pkg/jetstreamconsumer ./pkg/jetstreamconsumer
COPY services/notifier-consumer ./services/notifier-consumer
RUN go mod download && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/notifier-consumer ./services/notifier-consumer/cmd/server && mkdir /out/data

FROM scratch
COPY --from=build /out/notifier-consumer /notifier-consumer
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
EXPOSE 8086
ENTRYPOINT ["/notifier-consumer"]
