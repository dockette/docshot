<h1 align=center>Dockette / Docshot</h1>

<p align=center>
   📸 One HTTP call turns any document, spreadsheet, deck, e-mail, archive, audio or video into a picture.
</p>

<p align=center>
🕹 <a href="https://f3l1x.io">f3l1x.io</a> | 💻 <a href="https://github.com/f3l1x">f3l1x</a> | 🐦 <a href="https://twitter.com/xf3l1x">@xf3l1x</a>
</p>

<p align=center>
  <a href="https://hub.docker.com/r/dockette/docshot/"><img src="https://badgen.net/docker/pulls/dockette/docshot"></a>
  <a href="https://bit.ly/ctteg"><img src="https://badgen.net/badge/support/gitter/cyan"></a>
  <a href="https://github.com/sponsors/f3l1x"><img src="https://badgen.net/badge/sponsor/donations/F96854"></a>
</p>

![](.docs/preview.png "docshot output: docx, pptx, xlsx, eml, markdown, zip")

-----

Upload a document, get back a picture: one page, a contact sheet, or a ZIP of every page.
300+ formats — PDF, Office, HTML, Markdown, e-mail, images, video, audio, archives, fonts.
Self-hosted, one container. Great for thumbnails, previews and vision LLMs.

## Quick start

```sh
docker run -d -p 3000:3000 dockette/docshot
```

```sh
curl -F file=@report.docx localhost:3000/upload       -o report.png   # first page
curl -F file=@deck.pptx   localhost:3000/upload/grid  -o deck.png     # contact sheet
curl -F file=@report.docx localhost:3000/upload/pages -o report.zip   # all pages
```

## Usage

| | Upload (`POST`) | Link (`GET`) |
|---|---|---|
| One page | `/upload` | `/render` |
| Contact sheet | `/upload/grid` | `/render/grid` |
| All pages (ZIP) | `/upload/pages` | `/render/pages` |
| Metadata (JSON) | `/upload/info` | `/info` |

Common parameters: `page`, `width`, `height`, `fit`, `format` (`png`, `jpeg`, `webp`, `auto`)
and `preset` (`thumb`, `card`, `preview`, `og`).

```sh
curl -F file=@movie.mp4 'localhost:3000/upload?time=1:23&width=640&format=webp' -o frame.webp
```

`GET` links work straight in `<img>`, once a source is enabled:

```html
<img src="http://localhost:3000/render?path=offer.docx&preset=thumb">
```

| Source | Enable with |
|---|---|
| `?path=…` | `DOCSHOT_ROOT` + a mounted directory |
| `?s3=bucket/key` | `DOCSHOT_S3_*` |
| `?url=https://…` | `DOCSHOT_FETCH_ALLOW=<regexp>` |

Errors are JSON (`{"error", "code", "status"}`). `GET /formats` lists every supported
extension; `/healthz`, `/readyz`, `/metrics` and `/version` are there for monitoring.

## Configuration

| Env | Default | |
|---|---|---|
| `DOCSHOT_DEFAULT_DPI` | `150` | |
| `DOCSHOT_MAX_BYTES` | `104857600` | Upload limit |
| `DOCSHOT_TIMEOUT_SECONDS` | `180` | |
| `DOCSHOT_CACHE_MB` | `256` | In-memory cache |
| `DOCSHOT_ROOT` | – | Directory for `?path=` |
| `DOCSHOT_FETCH_ALLOW` | – | URLs `?url=` may fetch |
| `DOCSHOT_SIGN_KEY` | – | Require signed `GET` links |

All options are in [`internal/server/server.go`](internal/server/server.go).

> [!WARNING]
> There is no authentication. Keep docshot on a private network, or set `DOCSHOT_SIGN_KEY`.

## How it works

A small Go server on `:3000` runs [Gotenberg](https://gotenberg.dev) inside the same
container. Documents become PDF (LibreOffice, Chromium) and PDFium draws the page;
images, video and audio go through Go and ffmpeg. Truncated or corrupted images show
what survived, like in a browser. JavaScript and network access are
off for untrusted input.

## Versions

Releases are dated: `YYYY.MM.DD` (see [`VERSION`](VERSION)).

| Tag | |
|---|---|
| `dockette/docshot:latest` | latest `master` |
| `dockette/docshot:2026.10.19` | a release — pin this |
| `dockette/docshot:sha-<sha>` | exact commit |

To release, run `make release`, commit and push; CI publishes the image and the GitHub release.

## Development

```sh
make up          # build + start on :9994
make test-unit   # go vet + unit tests
make lint        # gofmt, staticcheck, govulncheck, shellcheck, hadolint
make smoke       # render one page with the built image
make test        # end-to-end checks
```

See [how to contribute](https://contributte.org/contributing.html) to this package.

This package is currently maintaining by these authors.

<a href="https://github.com/f3l1x">
    <img width="80" height="80" src="https://avatars2.githubusercontent.com/u/538058?v=3&s=80">
</a>

-----

Consider to [support](https://github.com/sponsors/f3l1x) **f3l1x**. Also thank you for using this package.
