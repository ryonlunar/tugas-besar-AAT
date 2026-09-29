FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY services/bmkg-mock ./services/bmkg-mock
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bmkg-mock ./services/bmkg-mock/cmd/server

FROM scratch
COPY --from=build /out/bmkg-mock /bmkg-mock
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/bmkg-mock"]
