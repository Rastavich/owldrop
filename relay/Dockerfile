# Build stage
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN go build -o /out/relay .

# Runtime stage — no compilers, just the binary + CA certs.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/relay /usr/local/bin/relay
ENV PORT=8080 DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["relay"]
