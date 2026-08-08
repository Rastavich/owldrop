# Owldrop headless server image — for NAS boxes (Unraid et al.) that run
# tailscaled on the host: mount the daemon socket + a config/save volume,
# open the UI from any device on your tailnet.
#
#   docker run -d --name owldrop \
#     -p 8976:8976 \
#     -v /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock \
#     -v owldrop-config:/data \
#     -v /mnt/user/downloads:/data/downloads \
#     ghcr.io/rastavich/owldrop:latest --lan --save-dir /data/downloads
#
# The binary is fully static (CGO_ENABLED=0, -tags server): no GTK, no
# libc dependencies at runtime.
FROM golang:alpine AS builder
ARG VERSION=dev
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN sed -i '/^replace/d' go.mod || true
RUN CGO_ENABLED=0 go build -tags server,production -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.appVersion=${VERSION}" -o /owldrop .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=builder /owldrop /owldrop
ENV HOME=/data
EXPOSE 8976
VOLUME /data
ENTRYPOINT ["/owldrop"]
CMD ["--lan", "--port", "8976"]
