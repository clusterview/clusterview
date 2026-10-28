# syntax=docker/dockerfile:1

# 1. Build the UI.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Build the binary with the UI embedded. Cross-compiles, so the multi-arch
#    build doesn't run under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/clusterview/clusterview/internal/version.Version=${VERSION} -X github.com/clusterview/clusterview/internal/version.Commit=${COMMIT}" \
    -o /out/clusterview ./cmd/clusterview

# 3. Distroless, non-root, nothing but the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/clusterview /clusterview
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/clusterview"]
