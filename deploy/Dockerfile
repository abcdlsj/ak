# ak as a service in a container. From the repository root:
#   docker build -f deploy/Dockerfile -t ak .
#   docker run -d --name ak -v ak:/data -p 127.0.0.1:8787:8787 ak
# Inside, service.addr must be 0.0.0.0:8787, which ak refuses while no token
# is defined: issue one first,
#   docker run --rm -it -v ak:/data ak serve token add laptop
# and put TLS in front (deploy/Caddyfile).
FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ak .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /ak /ak
ENV AK_CONFIG_DIR=/data HOME=/data
VOLUME /data
EXPOSE 8787
ENTRYPOINT ["/ak"]
CMD ["serve", "--service", "0.0.0.0:8787"]
