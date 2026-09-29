FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY services/pvmbg-mock ./services/pvmbg-mock
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pvmbg-mock ./services/pvmbg-mock/cmd/server

FROM scratch
COPY --from=build /out/pvmbg-mock /pvmbg-mock
USER 65532:65532
EXPOSE 8082
ENTRYPOINT ["/pvmbg-mock"]
