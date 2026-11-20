# syntax=docker/dockerfile:1
# AK as a service in a container. From the repository root:
#
#   docker build -f deploy/Dockerfile --build-arg VERSION=v1.2.3 -t ghcr.io/abcdlsj/ak .
#   docker run -d --name ak -v ak:/data -p 127.0.0.1:8787:8787 ghcr.io/abcdlsj/ak
#
# Inside, service.addr is 0.0.0.0:8787, which AK refuses while no token is
# defined, so issue one first:
#
#   docker run --rm -it -v ak:/data ghcr.io/abcdlsj/ak serve token add laptop
#   docker run --rm -it -v ak:/data ghcr.io/abcdlsj/ak serve route add main --member …
#
# Then put TLS in front (deploy/Caddyfile); everything else is the CLI.
#
# GOPROXY is a build arg so a build behind a slow network can point at a
# mirror: --build-arg GOPROXY=https://goproxy.cn,direct

FROM golang:1.27.4-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w -X github.com/abcdlsj/ak/internal/version.Version=${VERSION}" -o /ak .

# Alpine rather than distroless: it carries the CA bundle AK needs to reach a
# vendor over TLS, and a shell to look at the container with.
FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 65532 ak \
 && adduser -S -u 65532 -G ak -h /data ak \
 && mkdir -p /data && chown ak:ak /data
COPY --from=build /ak /ak
ENV AK_CONFIG_DIR=/data HOME=/data
USER ak
VOLUME /data
EXPOSE 8787
ENTRYPOINT ["/ak"]
CMD ["serve", "--service", "0.0.0.0:8787"]
