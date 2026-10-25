# Changelog

Versions are release dates (`YYYY.MM.DD`, `.N` for a same-day re-release); each one is the
`dockette/docshot:<version>` image and the GitHub release of the same name. See README → *Versions*.

## 2026.10.25

### Fixed

- Images are detected by content when the extension is wrong or missing (`image.php`, `scan.pdf`
  holding a PNG/JPEG/GIF/BMP/WebP/TIFF…): they render as images instead of text or a failed
  conversion. RAW camera files keep their extension.

### Changed

- The image applies Debian security updates on top of the Gotenberg base at build time, and so on
  every weekly rebuild: 659 fixable HIGH/CRITICAL CVEs in Chromium, LibreOffice, OpenSSL… down to 0.
- The image is a single flattened layer, so upgraded packages replace the base's copies instead of
  stacking on them: 2.75 GB (layered with the upgrade it would be 3.99 GB). A pull downloads the
  whole layer once per release instead of sharing layers with `gotenberg/gotenberg`.

## 2026.10.24

### Breaking

- With `DOCSHOT_SIGN_KEY` set, `POST /upload*` needs `?sign=` too; signed uploads take their
  parameters from the URL only (form fields are a `400`).
- `DOCSHOT_FETCH_ALLOW` is anchored at the start: `example\.com` no longer matches
  `https://example.com/…`; write `^https://example\.com/`.
- `?url=` refuses loopback, link-local (cloud metadata) and unspecified addresses, whatever the
  allow-list says.
- The `?path=` source and `DOCSHOT_ROOT` are gone; `?path=` is an unknown parameter (`400`).
- Settings that do not parse (`DOCSHOT_TIMEOUT_SECONDS=3m`, `DOCSHOT_CACHE_TAGS=yes`) stop docshot
  at startup instead of falling back to the default.

### Added

- Disk cache of rendered results keyed by URL (`DOCSHOT_CACHE_DIR`, `DOCSHOT_CACHE_SIZE`), kept
  across restarts; `DOCSHOT_CACHE_TTL` with stale-while-revalidate and stale-if-error, `?v=` and
  `DOCSHOT_CACHE_BUSTER`, `DELETE /cache`, ETag / `Last-Modified` / `Cache-Status` /
  `CDN-Cache-Control` / `Surrogate-Key` headers.
- Prometheus metrics on `/metrics`: input types, sizes, pages, durations, cache, S3, HTTP, Go
  runtime.
- Truncated and corrupted images render what survived, like a browser (PNG, progressive JPEG,
  GIF/WebP animations, BMP, ICO).
- Signed URLs can expire: `exp=`, or `docshot -sign … -expires 24h`.
- `DOCSHOT_UPLOAD_MEMORY` (default `1GB`): uploads held in memory at once; beyond it requests wait,
  then `429`.
- `DOCSHOT_LOG_FORMAT=json`.
- `DOCSHOT_MAX_BYTES` takes units (`100MB`, `1GiB`); `DOCSHOT_DEFAULT_WIDTH` /
  `DOCSHOT_DEFAULT_HEIGHT` for requests without a size.
- Images carry an SBOM, provenance and a keyless cosign signature.

### Fixed

- A Gotenberg crash or restart no longer kills in-flight video, audio, archive and RAW renders
  (the supervisor killed every child process, not just Gotenberg's).
- `docker stop` drains in-flight requests before Gotenberg stops; compose allows 60 s for it.
- A malformed Outlook / legacy Office file could crash the parser, and in a background cache
  re-check the whole process; a panic while rendering is now a `500`.
- A small `.msg` could make docshot allocate up to 4 GiB (RTF body size taken from the file).
- Upload bodies and form fields are bounded; slow uploads time out.
- Documents are no longer copied twice in memory on their way to Gotenberg.
- ffmpeg: a truncated JPEG XL no longer loops at 100% CPU or panics; stderr is capped at 4 KiB.
- Built with Go 1.27.3: `crypto/x509` fix for CVE-2026-111943 (GO-2026-6809).

### Changed

- compose drops all capabilities and sets `no-new-privileges`.
- CI: actions pinned to commit SHAs and kept current by Dependabot, Trivy scan of the image,
  govulncheck pinned.

## 2026.10.19

First release: any document (PDF, Office, HTML, Markdown, e-mail, archives, images, audio, video,
fonts…) as one page in PNG/JPEG/WebP, a contact sheet or a ZIP of all pages, over HTTP; Gotenberg
bundled and supervised in one image; date-based versions.
