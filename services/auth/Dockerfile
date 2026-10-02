FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY services/auth ./services/auth
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/auth ./services/auth/cmd/server
RUN mkdir -p /out/data

FROM scratch
COPY --from=build /out/auth /auth
COPY --chown=65532:65532 --from=build /out/data /data
USER 65532:65532
EXPOSE 8084
ENTRYPOINT ["/auth"]
