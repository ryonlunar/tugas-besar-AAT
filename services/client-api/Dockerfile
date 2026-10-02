FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY services/client-api ./services/client-api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/client-api ./services/client-api/cmd/server

FROM scratch
COPY --from=build /out/client-api /client-api
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/client-api"]
