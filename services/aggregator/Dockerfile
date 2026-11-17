FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY pkg/telemetry ./pkg/telemetry
RUN go mod download
COPY contracts ./contracts
COPY services/aggregator ./services/aggregator
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/aggregator ./services/aggregator/cmd/server

FROM scratch
COPY --from=build /out/aggregator /aggregator
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 8083
ENTRYPOINT ["/aggregator"]
