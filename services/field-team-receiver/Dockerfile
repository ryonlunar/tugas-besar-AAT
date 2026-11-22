FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY contracts ./contracts
COPY pkg/telemetry ./pkg/telemetry
COPY services/field-team-receiver ./services/field-team-receiver
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/field-team-receiver ./services/field-team-receiver

FROM scratch
COPY --from=build /out/field-team-receiver /field-team-receiver
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 8087
ENTRYPOINT ["/field-team-receiver"]
