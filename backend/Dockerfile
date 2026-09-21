# Build Go backend.
# If go.sum is absent, -mod=mod allows the build to resolve modules.
# For reproducible production builds, commit go.sum and use go mod verify.

FROM golang:1.27-alpine AS builder

WORKDIR /src

# Copy module file first for better layer caching.
COPY go.mod ./
# COPY go.sum ./

# Copy source code.
COPY . .

# Build static binary.
RUN CGO_ENABLED=0 GOOS=linux go build -mod=mod -trimpath -o /out/api ./cmd/api

FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/api /usr/local/bin/api

EXPOSE 8080

ENTRYPOINT ["api"]