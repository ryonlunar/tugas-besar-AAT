FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY contracts ./contracts
COPY pkg/jetstreamconsumer ./pkg/jetstreamconsumer
COPY services/dashboard-consumer ./services/dashboard-consumer
RUN go mod download && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/dashboard-consumer ./services/dashboard-consumer/cmd/server && mkdir /out/data

FROM scratch
COPY --from=build /out/dashboard-consumer /dashboard-consumer
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
EXPOSE 8085
ENTRYPOINT ["/dashboard-consumer"]
