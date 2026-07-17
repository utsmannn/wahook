# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/wahook .

FROM --platform=$BUILDPLATFORM alpine:3.21 AS compress
RUN apk add --no-cache upx
COPY --from=build /out/wahook /out/wahook
RUN upx --best --lzma /out/wahook

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=compress /out/wahook /usr/local/bin/wahook
ENTRYPOINT ["/usr/local/bin/wahook"]
CMD ["-config", "/config/config.yaml"]
