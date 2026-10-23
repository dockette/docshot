# AGENTS.md

## Project

Dockette Docshot builds `dockette/docshot`, an HTTP service that renders any document (PDF, Office, HTML, Markdown, e-mail, archives, images, audio, video, fonts…) into one page as PNG/JPEG/WebP, a contact sheet, or a ZIP of all pages.

## Images

- Default image: `dockette/docshot:latest`.
- Build context: repository root `.` with `Dockerfile` (multi-stage: `golang:1.27-trixie` build → `gotenberg/gotenberg` runtime).
- Date versions: `VERSION` holds `YYYY.MM.DD` (`.N` for a same-day re-release), bumped by `make release`; it's built into the binary (`main.version`, via `-ldflags -X`), tags images `latest`, `<VERSION>` and `sha-<sha>`, and CI creates the matching GitHub release. See README → *Development*.
- The Go binary (no CGO, `cmd/docshot` + `internal/*`) listens on `:3000` and supervises Gotenberg as a child on `127.0.0.1:3001`; the image `CMD` is Gotenberg's own command line.

## Layout

- `cmd/docshot` — flags, env config, HTTP server startup, the Gotenberg supervisor.
- `internal/server` — HTTP API: handlers, parameters and presets, sources (`s3`, `url`), S3 store, signing, `/metrics`.
- `internal/render` — the pipeline: caches, concurrency limit, conversion to PDF (Gotenberg, tools), page/grid/ZIP output, archives.
- `internal/detect` — file type and route by extension or magic bytes (OLE2 streams for doc/xls/ppt/msg). No Apache Tika at runtime.
- `internal/imaging` — geometry, trim, grid, encoding, PDFium rasterizer, page sequences, placeholder.
- `internal/media` — ffmpeg/ffprobe (video, audio, exotic images), LibRaw, tiff2pdf/ddjvu/xpstopdf/dot.
- `internal/htmlconv` — formats rendered as an HTML page: e-mail, Outlook msg, EPUB, CSV, JSON, notebooks, calendars, contacts, fonts, SVG, text.
- `internal/diskcache` — byte-bounded LRU of rendered results on disk (`DOCSHOT_CACHE_DIR`), index rebuilt on start; `internal/server/cache.go` keys it by URL and handles ETag/TTL/purge.
- `internal/apierr`, `internal/metrics`, `internal/lru`, `internal/supervisor` — JSON errors, Prometheus collectors (client_golang), byte-bounded LRU, child-process supervision.
- GitHub Actions runs lint + unit tests, a smoke test of the built image, then publishes `linux/amd64,linux/arm64` on `master` and the weekly schedule, and creates a GitHub release when `VERSION` is new.

## Commands

- `make build` builds `${DOCKER_IMAGE}` from `.`, stamped with `VERSION`.
- `make version` prints `VERSION`; `make release` sets it to today's date (UTC).
- `make test-unit` runs `go vet` and `go test -race` with `-tags nodynamic` in a `golang` container (the code uses Linux-only syscalls and does not compile on macOS).
- `make lint` runs gofmt, staticcheck, govulncheck, shellcheck and hadolint.
- `make smoke` starts `${DOCKER_IMAGE}`, waits for `/readyz` and renders one Markdown page.
- `make up` / `make down` run the compose stack on `:9994`.
- `make test` (`test-params` + `test-all`) and `make test-formats` are end-to-end checks against a running docshot (`:9994`); they use an Apache Tika server (`:9998`) only as an OCR oracle to verify the rendered images with fixtures from `../tika/fixtures` (`FIXTURES=`).

## Testing Notes

- Always build Go with `-tags nodynamic` (go-pdfium WebAssembly backend, no CGO).
- Run `make test-unit` after Go changes and `make smoke` after Dockerfile changes.
- `.env.dist` is the compose config template; copy it to `.env` (git-ignored).

## Guidelines

- Keep `Dockerfile`, `Makefile`, `compose.yml`, README and `.github/workflows/docker.yml` aligned.
- Prefer `DOCKER_*` names for Docker-related Makefile variables.
- Keep README badges and maintenance sections consistent with other Dockette image repos.
- Errors are JSON `{"error", "code", "status"}`; unknown or misplaced query parameters are a `400`.
- Do not introduce unrelated formatting or structural changes.
