################################################################################
# docshot: the Go binary listens on :3000 and supervises Gotenberg on
# 127.0.0.1:3001 (internal). Three stages: build -> runtime -> flattened final.
################################################################################

ARG GOTENBERG_VERSION=8.37


################################################################################
# Stage 1: build the Go binary (no CGO)
################################################################################

FROM golang:1.27-trixie AS build

WORKDIR /src

# Dependencies first, so they stay cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /docshot ./cmd/docshot


################################################################################
# Stage 2: runtime packages on top of Gotenberg
################################################################################

FROM gotenberg/gotenberg:${GOTENBERG_VERSION} AS runtime

USER root

# - apt-get upgrade: Debian security updates (Chromium, LibreOffice, OpenSSL...) without waiting for a Gotenberg rebuild
# - ffmpeg and the per-format helpers (tiff, archives, djvu, xps, dot, raw)
# - libwebp.so symlinks: gen2brain/webp dlopens them for native WebP encoding
# - /tmp/docshot: disk cache, owned by the runtime user so a named volume inherits it
RUN apt-get update \
 && apt-get upgrade -y --no-install-recommends \
 && apt-get install -y --no-install-recommends ffmpeg libwebp7 libwebpdemux2 \
      libtiff-tools libarchive-tools djvulibre-bin libgxps-utils graphviz libraw-bin \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* \
 && lib="$(dirname /usr/lib/*-linux-gnu/libwebp.so.7)" \
 && ln -sf libwebp.so.7 "$lib/libwebp.so" && ln -sf libwebpdemux.so.2 "$lib/libwebpdemux.so" \
 && install -d -o gotenberg -g gotenberg /tmp/docshot

COPY --from=build /docshot /usr/bin/docshot

USER gotenberg


################################################################################
# Stage 3: flatten into one layer
#
# As separate layers, the upgraded Chromium and LibreOffice would sit on top of
# the base's old copies (~1 GB). FROM scratch drops the base image's config, so
# its ENV and WORKDIR are restated below: re-check them after a
# GOTENBERG_VERSION bump.
################################################################################

FROM scratch

# hadolint ignore=DL3067
COPY --from=runtime / /

# Restated from the Gotenberg base image.
ENV PATH=/opt/java/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    TZ=UTC \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    PDFTK_BIN_PATH=/usr/bin/pdftk \
    QPDF_BIN_PATH=/usr/bin/qpdf \
    EXIFTOOL_BIN_PATH=/usr/bin/exiftool \
    PDFCPU_BIN_PATH=/usr/bin/pdfcpu \
    GOTENBERG_VERSIONS_DIR_PATH=/opt/gotenberg/versions \
    OTEL_TRACES_EXPORTER=none \
    OTEL_METRICS_EXPORTER=none \
    OTEL_LOGS_EXPORTER=none \
    CHROMIUM_BIN_PATH=/usr/bin/chromium \
    CHROMIUM_HYPHEN_DATA_DIR_PATH=/opt/gotenberg/chromium-hyphen-data \
    LIBREOFFICE_BIN_PATH=/usr/lib/libreoffice/program/soffice.bin \
    UNOCONVERTER_BIN_PATH=/usr/bin/unoconverter
WORKDIR /home/gotenberg

ARG VERSION=dev
LABEL org.opencontainers.image.version=$VERSION

USER gotenberg
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 CMD ["/usr/bin/docshot", "-healthcheck"]
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/bin/docshot"]


################################################################################
# Gotenberg's command line, as docshot starts it for untrusted input:
# no JavaScript, no network (public and private IPs), no downloadFrom, no webhooks.
################################################################################

CMD ["gotenberg", \
     "--chromium-disable-javascript=true", \
     "--chromium-deny-public-ips=true", "--chromium-deny-private-ips=true", \
     "--libreoffice-deny-public-ips=true", "--libreoffice-deny-private-ips=true", \
     "--api-disable-download-from=true", "--webhook-disable=true", \
     "--api-timeout=150s", "--log-level=warn"]
