# All-in-one: docshot on the Gotenberg image; the Go binary listens on :3000 and supervises Gotenberg on 127.0.0.1:3001 (internal).
ARG GOTENBERG_VERSION=8.37

FROM golang:1.27-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /docshot ./cmd/docshot

FROM gotenberg/gotenberg:${GOTENBERG_VERSION}
# ffmpeg and per-format helpers; /tmp/docshot is the disk cache (DOCSHOT_CACHE_DIR), owned by the runtime user so a named volume inherits it. libwebp symlinks: gen2brain/webp dlopens libwebp.so via purego for faster native encoding.
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg libwebp7 libwebpdemux2 \
      libtiff-tools libarchive-tools djvulibre-bin libgxps-utils graphviz libraw-bin \
 && rm -rf /var/lib/apt/lists/* \
 && lib="$(dirname /usr/lib/*-linux-gnu/libwebp.so.7)" \
 && ln -sf libwebp.so.7 "$lib/libwebp.so" && ln -sf libwebpdemux.so.2 "$lib/libwebpdemux.so" \
 && install -d -o gotenberg -g gotenberg /tmp/docshot
COPY --from=build /docshot /usr/bin/docshot
ARG VERSION=dev
LABEL org.opencontainers.image.version=$VERSION
USER gotenberg
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 CMD ["/usr/bin/docshot", "-healthcheck"]
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/bin/docshot"]
# Defaults for untrusted input: no JavaScript, no network for Chromium/LibreOffice, no downloadFrom/webhooks (SSRF).
CMD ["gotenberg", \
     "--chromium-disable-javascript=true", \
     "--chromium-deny-public-ips=true", "--chromium-deny-private-ips=true", \
     "--libreoffice-deny-public-ips=true", "--libreoffice-deny-private-ips=true", \
     "--api-disable-download-from=true", "--webhook-disable=true", \
     "--api-timeout=150s", "--log-level=warn"]
