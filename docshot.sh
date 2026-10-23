#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# docshot.sh — exercise the docshot service the way a real caller would.
#
#   ./docshot.sh render <file> [page] [out.png]   # one page → image file (POST /upload)
#   ./docshot.sh test-all [outdir]                # every fixture, sentinel-checked via Tika OCR
#   ./docshot.sh test-params                      # page / format / width / error codes
#   ./docshot.sh health | formats
#
# test-all is end-to-end: Tika reads the SENTINEL token from the original file,
# Tika OCRs the rendered PNG, and the two must match. A blank or garbled page
# fails even though the HTTP status was 200.
# ---------------------------------------------------------------------------
set -uo pipefail

HOST="${DOCSHOT_HOST:-http://localhost:9994}"
TIKA="${TIKA_HOST:-http://localhost:9998}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIX="${FIXTURES:-$DIR/../tika/fixtures}"

if [[ -t 1 ]]; then R=$'\e[31m'; G=$'\e[32m'; Y=$'\e[33m'; D=$'\e[2m'; N=$'\e[0m'; else R=; G=; Y=; D=; N=; fi
die() { echo "${R}error:${N} $*" >&2; exit 1; }

need_up() {
  curl -fsS -m 5 "$HOST/readyz" >/dev/null 2>&1 || die "docshot is not answering at $HOST — cd $DIR && make up"
}

# render <file> <out> [query] — prints "status path pages ms"
render() {
  local file="$1" out="$2" q="${3:-}"
  local hdrs; hdrs="$(mktemp)"
  local code
  code=$(curl -sS -m 300 -F "file=@$file" \
    -D "$hdrs" -o "$out" -w '%{http_code}' "$HOST/upload${EP:-}${q:+?$q}")
  local path pages ms
  path=$(grep -i '^x-render-path:' "$hdrs" | tr -d '\r' | cut -d' ' -f2)
  pages=$(grep -i '^x-page-count:' "$hdrs" | tr -d '\r' | cut -d' ' -f2)
  ms=$(grep -i '^x-render-ms:' "$hdrs" | tr -d '\r' | cut -d' ' -f2)
  rm -f "$hdrs"
  echo "$code ${path:--} ${pages:--} ${ms:--}"
}

sentinel_of() {
  curl -sS -m 120 -T "$1" -H "Accept: text/plain" \
    -H "Content-Disposition: attachment; filename=$(basename "$1")" "$TIKA/tika" 2>/dev/null \
    | grep -oE 'SENTINEL-[A-Z0-9]+-[0-9a-f]{4}' | sort -u
}

ocr_of() {
  curl -sS -m 120 -T "$1" -H "Accept: text/plain" -H "X-Tika-OCRLanguage: eng" \
    -H "Content-Disposition: attachment; filename=$(basename "$1")" "$TIKA/tika" 2>/dev/null
}

# OCR reads "0" for "O" and drops hyphens now and then; compare loosely.
norm() { tr 'a-z' 'A-Z' | tr -d ' \n-' | tr 'O' '0'; }

cmd="${1:-help}"; shift || true
case "$cmd" in
  health) curl -fsS "$HOST/readyz"; echo; curl -fsS "$HOST/version" ;;
  formats) curl -fsS "$HOST/formats" | python3 -m json.tool ;;
  render)
    [[ $# -ge 1 ]] || die "usage: $0 render <file> [page] [out.png]"
    need_up
    f="$1"; p="${2:-1}"; out="${3:-$(basename "${f%.*}")-p$p.png}"
    read -r code path pages ms < <(render "$f" "$out" "page=$p")
    [[ "$code" == 200 ]] || { cat "$out"; echo; rm -f "$out"; die "HTTP $code"; }
    echo "$out  ${D}(path=$path pages=$pages ${ms}ms)${N}"
    ;;
  test-all)
    need_up
    curl -fsS -m 5 "$TIKA/version" >/dev/null || die "Tika ($TIKA) is needed for the sentinel check"
    out="${1:-$(mktemp -d)}"; mkdir -p "$out"
    pass=0 fail=0 skip=0
    printf '%-24s %-6s %-18s %5s %7s  %s\n' FILE HTTP PATH PAGES MS RESULT
    for f in "$FIX"/*; do
      b="$(basename "$f")"
      case "$b" in *.py|*.sh) [[ "$b" == sample.py ]] || continue ;; esac
      png="$out/${b}.png"
      read -r code path pages ms < <(render "$f" "$png")
      if [[ "$code" != 200 ]]; then
        printf '%-24s %-6s %-18s %5s %7s  %s\n' "$b" "$code" "$path" "$pages" "$ms" "${Y}no image${N} $(head -c 120 "$png")"
        skip=$((skip+1)); rm -f "$png"; continue
      fi
      want="$(sentinel_of "$f")"
      if [[ -z "$want" ]]; then
        # No sentinel to match, but the page must not be blank: OCR has to
        # read *something* (photos and scans without text are the exception).
        if [[ -n "$(ocr_of "$png" | tr -d '[:space:]')" ]]; then
          printf '%-24s %-6s %-18s %5s %7s  %s\n' "$b" "$code" "$path" "$pages" "$ms" "${G}ok${N} ${D}(text on page, no sentinel)${N}"
          pass=$((pass+1))
        else
          printf '%-24s %-6s %-18s %5s %7s  %s\n' "$b" "$code" "$path" "$pages" "$ms" "${R}BLANK?${N} OCR found no text"
          fail=$((fail+1))
        fi
        continue
      fi
      # Containers (archives) hold several sentinels; page 1 must show one of them.
      got="$(ocr_of "$png" | norm)" hit=""
      for w in $want; do [[ "$got" == *"$(echo "$w" | norm)"* ]] && hit=$w && break; done
      if [[ -n "$hit" ]]; then
        printf '%-24s %-6s %-18s %5s %7s  %s\n' "$b" "$code" "$path" "$pages" "$ms" "${G}PASS${N} $hit"
        pass=$((pass+1))
      else
        printf '%-24s %-6s %-18s %5s %7s  %s\n' "$b" "$code" "$path" "$pages" "$ms" "${R}FAIL${N} $(echo $want) not on page 1"
        fail=$((fail+1))
      fi
    done
    echo "pass=$pass fail=$fail no-image=$skip   images in $out"
    [[ $fail -eq 0 ]]
    ;;
  test-params)
    need_up
    t="$(mktemp -d)"; ok=0 bad=0
    check() { # name expected-code actual-code
      if [[ "$2" == "$3" ]]; then echo "${G}ok${N}   $1"; ok=$((ok+1)); else echo "${R}FAIL${N} $1 (want $2, got $3)"; bad=$((bad+1)); fi
    }
    pdf="$FIX/mixed-report.pdf"
    read -r c _ pages _ < <(render "$pdf" "$t/a.png");            check "pdf page 1" 200 "$c"
    read -r c _ _ _ < <(render "$pdf" "$t/b.png" "page=$pages");    check "pdf last page ($pages)" 200 "$c"
    read -r c _ _ _ < <(render "$pdf" "$t/c" "page=$((pages+1))");  check "page past the end → 416" 416 "$c"
    read -r c _ _ _ < <(render "$pdf" "$t/d.webp" "format=webp");   check "format=webp" 200 "$c"
    [[ "$(head -c 4 "$t/d.webp")" == RIFF ]] && check "webp magic" x x || check "webp magic" RIFF "?"
    read -r c _ _ _ < <(render "$pdf" "$t/e.jpg" "format=jpeg&quality=70"); check "format=jpeg" 200 "$c"
    read -r c _ _ _ < <(render "$pdf" "$t/f.png" "width=400");      check "width=400" 200 "$c"
    w=$(python3 -c "import struct,sys;d=open(sys.argv[1],'rb').read(24);print(struct.unpack('>I',d[16:20])[0])" "$t/f.png")
    check "width=400 → 400px" 400 "$w"
    dims() { python3 -c "import struct,sys;d=open(sys.argv[1],'rb').read(24);print('%dx%d'%struct.unpack('>II',d[16:24]))" "$1"; }
    read -r c _ _ _ < <(render "$pdf" "$t/r1.png" "width=300&height=300&fit=cover&position=top"); check "pdf cover 300x300 top" 300x300 "$(dims "$t/r1.png")"
    read -r c _ _ _ < <(render "$pdf" "$t/r2.png" "width=300&height=300");      check "pdf contain within 300x300" 1 "$(dims "$t/r2.png" | awk -Fx '{print ($1<=300 && $2<=300 && ($1==300||$2==300))}')"
    read -r c _ _ _ < <(render "$pdf" "$t/r3.png" "width=200&height=100&fit=fill"); check "pdf fill 200x100" 200x100 "$(dims "$t/r3.png")"
    read -r c _ _ _ < <(render "$FIX/invoice.png" "$t/r4.png" "height=200");  check "image height=200 keeps aspect" 200 "$(dims "$t/r4.png" | cut -dx -f2)"
    read -r c _ _ _ < <(render "$FIX/receipt.jpg" "$t/r5.png" "width=128&height=128&fit=cover"); check "image cover 128x128" 128x128 "$(dims "$t/r5.png")"
    read -r c _ _ _ < <(render "$pdf" "$t/r6" "fit=zoom");                    check "bad fit → 400" 400 "$c"
    read -r c _ _ _ < <(render "$FIX/deck.pptx" "$t/p1.png" "width=400&height=300&fit=pad&pad=20&bg=eeeeee"); check "fit=pad exact canvas" 400x300 "$(dims "$t/p1.png")"
    read -r c _ _ _ < <(render "$FIX/deck.pptx" "$t/p2.png" "trim=1&width=400"); check "trim keeps requested width" 400 "$(dims "$t/p2.png" | cut -dx -f1)"
    read -r c _ _ _ < <(render "$FIX/deck.pptx" "$t/p3.png" "width=400"); [[ "$(dims "$t/p2.png")" != "$(dims "$t/p3.png")" ]] && check "trim changes aspect (margins cut)" x x || check "trim changes aspect" differs same
    read -r c _ _ _ < <(render "$pdf" "$t/p4.webp" "preset=thumb&dpr=2");  check "preset=thumb&dpr=2 → 600x800" 600x800 "$(python3 -c "import sys;d=open(sys.argv[1],'rb').read(30);print('%dx%d'%(int.from_bytes(d[26:28],'little')&0x3fff,int.from_bytes(d[28:30],'little')&0x3fff))" "$t/p4.webp")"
    read -r c _ _ _ < <(render "$pdf" "$t/p5" "preset=nope");               check "unknown preset → 400" 400 "$c"
    ct=$(curl -s -o /dev/null -w '%{content_type}' -F "file=@$pdf" -H 'Accept: image/webp' "$HOST/upload?format=auto&width=200"); check "format=auto + Accept webp" image/webp "$ct"
    c=$(curl -s -o /dev/null -w '%{http_code} %{content_type}' "$HOST/render?url=http://127.0.0.1:1/missing.docx&fallback=1"); check "fallback placeholder for an error" "200 image/png" "$c"
    # The error contract: JSON with a stable code, never cached, for every failure.
    jc() { python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["status"],d["code"])'; }
    check "typo in a parameter → 400 unknown_parameter" "400 unknown_parameter" "$(curl -s -F "file=@$pdf" "$HOST/upload?widht=300" | jc)"
    curl -s -F "file=@$pdf" "$HOST/upload?widht=300" | grep -q 'did you mean \\"width\\"' && check "…with a suggestion" x x || check "…with a suggestion" width none
    check "limit on /upload → 400 parameter_not_applicable" "400 parameter_not_applicable" "$(curl -s -F "file=@$pdf" "$HOST/upload?limit=3" | jc)"
    check "unknown path → JSON 404" "404 not_found" "$(curl -s "$HOST/nope" | jc)"
    check "wrong method → JSON 405" "405 method_not_allowed" "$(curl -s "$HOST/upload" | jc)"
    check "corrupt PDF → 422 unreadable_file" "422 unreadable_file" "$(printf '%%PDF-1.4 garbage' | curl -s --data-binary @- "$HOST/upload?name=x.pdf" | jc)"
    c=$(curl -s -D - -o /dev/null -F "file=@$pdf" "$HOST/upload?page=99" | tr -d '\r' | grep -ci '^etag:'); check "errors carry no ETag" 0 "$c"
    check "/version is JSON" docshot "$(curl -s "$HOST/version" | python3 -c 'import json,sys;print(json.load(sys.stdin)["name"])')"
    c=$(curl -s -D - -o /dev/null -F "file=@$pdf" "$HOST/upload?download=report-p1.png" | tr -d '\r' | grep -i '^content-disposition:' | cut -d' ' -f2-); check "download=<name>" "attachment; filename=report-p1.png" "$c"
    pg=$(curl -s -F "file=@$pdf" "$HOST/upload/info" | python3 -c 'import json,sys;print(json.load(sys.stdin)["pages"])'); check "/info pages" 1 "$pg"
    curl -s "$HOST/metrics" | grep -q '^docshot_requests_total' && check "/metrics exposes counters" x x || check "/metrics" counters none
    c=$(curl -s -o /dev/null -w '%{http_code}' "$HOST/healthz"); check "/healthz" 200 "$c"
    c=$(curl -s -o /dev/null -w '%{http_code}' "$HOST/readyz"); check "/readyz" 200 "$c"
    c=$(curl -s -o /dev/null -w '%{http_code}' "$HOST/health"); check "/health removed → 404" 404 "$c"
    c=$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary "@$pdf" "$HOST/render"); check "PUT /render removed → 405" 405 "$c"
    c=$(curl -s -o /dev/null -w '%{http_code}' -F "files=@$pdf" "$HOST/forms/libreoffice/convert"); check "Gotenberg API not exposed → 404" 404 "$c"
    c=$(curl -s -o /dev/null -w '%{http_code} %{content_type}' -F "file=@$FIX/report.docx" -F width=200 -F format=webp "$HOST/upload"); check "multipart form fields as params" "200 image/webp" "$c"
    # Document contact sheet: 7-page text → grid=4x2 at 1200 px.
    for i in $(seq 1 400); do echo "line $i — grid test"; done > "$t/g.txt"
    read -r c _ _ _ < <(EP=/grid render "$t/g.txt" "$t/g.png" "grid=4x2&width=1200"); check "/upload/grid document 4x2 width" 1200 "$(dims "$t/g.png" | cut -dx -f1)"
    read -r c _ _ _ < <(EP=/grid render "$pdf" "$t/g1.png" "width=600");  check "/upload/grid auto on a 1-page pdf" 200 "$c"
    read -r c _ _ _ < <(render "$pdf" "$t/g2" "grid=2x2");             check "grid= on /upload → 400" 400 "$c"
    read -r c _ _ _ < <(EP=/pages render "$t/g.txt" "$t/p.zip" "width=300"); n=$(python3 -c "import zipfile,sys;print(len(zipfile.ZipFile(sys.argv[1]).namelist()))" "$t/p.zip"); check "/upload/pages ZIP of 7 pages" "200 7" "$c $n"
    read -r c _ _ _ < <(EP=/pages render "$t/g.txt" "$t/p2.zip" "limit=3&page=2"); n=$(python3 -c "import zipfile,sys;print(' '.join(zipfile.ZipFile(sys.argv[1]).namelist()))" "$t/p2.zip"); check "/upload/pages limit=3 from page 2" "0002.png 0003.png 0004.png" "$n"
    read -r c path _ _ < <(EP=/grid render "$FIX/archive.zip" "$t/a.png" "width=900"); check "/upload/grid of a zip ($path)" "200 archive" "$c $path"
    fl=$(curl -s -F "file=@$FIX/archive.zip" "$HOST/upload/info" | python3 -c 'import json,sys;print(",".join(json.load(sys.stdin)["files"]))'); check "zip /upload/info files" "inner.txt,plain.txt,report.docx" "$fl"
    read -r c path _ _ < <(render "$FIX/archive.zip" "$t/a2.png" "page=3&width=300"); check "zip page=3 = 3rd member (docx)" 200 "$c"
    # Video: a 5 s clip made by the container's own ffmpeg (there is none on the host).
    if docker exec docshot ffmpeg -v error -f lavfi -i testsrc=duration=5:size=320x240:rate=10 -c:v libvpx-vp9 -f webm - > "$t/v.webm" 2>/dev/null && [[ -s "$t/v.webm" ]]; then
      read -r c path _ _ < <(render "$t/v.webm" "$t/v0.png" "width=320");  check "video /upload = one frame ($path)" "200 320x240" "$c $(dims "$t/v0.png")"
      read -r c _ _ _ < <(EP=/grid render "$t/v.webm" "$t/v.png" "width=900"); check "video /upload/grid default 3x3" "200 900" "$c $(dims "$t/v.png" | cut -dx -f1)"
      read -r c _ _ _ < <(render "$t/v.webm" "$t/v1.png" "time=0:02&width=320"); check "video single frame time=0:02" "200 320x240" "$c $(dims "$t/v1.png")"
      read -r c _ _ _ < <(render "$t/v.webm" "$t/v2.png" "time=60");       check "video time past end → 416" 416 "$c"
      d=$(curl -s -F "file=@$t/v.webm" "$HOST/upload/info" | python3 -c 'import json,sys;print(json.load(sys.stdin)["duration_s"])'); check "video /upload/info duration" 5 "${d%.*}"
      # Animated GIF: 20 frames → grid spreads over them, /info counts them.
      docker exec docshot ffmpeg -v error -f lavfi -i testsrc=duration=2:size=160x120:rate=10 -f gif - > "$t/a.gif" 2>/dev/null
      fr=$(curl -s -F "file=@$t/a.gif" "$HOST/upload/info" | python3 -c 'import json,sys;print(json.load(sys.stdin)["pages"])'); check "animated gif /upload/info frames" 20 "$fr"
      read -r c path _ _ < <(EP=/grid render "$t/a.gif" "$t/ag.png" "grid=4x1&width=800"); check "animated gif /upload/grid" "200 800" "$c $(dims "$t/ag.png" | cut -dx -f1)"
      read -r c _ _ _ < <(render "$t/a.gif" "$t/a5.png" "page=5");    check "animated gif page=5 = frame 5" 200 "$c"
    else
      echo "${Y}skip${N} video/animation checks (docker exec docshot ffmpeg unavailable)"
    fi
    read -r c _ _ _ < <(render "$pdf" "$t/g" "dpi=5");              check "dpi out of range → 400" 400 "$c"
    read -r c _ _ _ < <(render "$pdf" "$t/h" "format=gif");         check "bad format → 400" 400 "$c"
    read -r c path _ _ < <(render "$FIX/sheet.xlsx" "$t/i.png");    check "xlsx one page per sheet ($path)" 200 "$c"
    # Multi-page: a long text file paginates to several A4 pages.
    for i in $(seq 1 400); do echo "line $i — PAGE-TEST žluťoučký kůň"; done > "$t/long.txt"
    read -r c _ pages _ < <(render "$t/long.txt" "$t/l1.png")
    [[ "${pages:-0}" -gt 1 ]] && check "long text paginates ($pages pages)" x x || check "long text paginates" ">1" "$pages"
    read -r c _ _ _ < <(render "$t/long.txt" "$t/l2.png" "page=2");  check "page=2 of $pages" 200 "$c"
    cmp -s "$t/l1.png" "$t/l2.png" && check "page 2 differs from page 1" different same || check "page 2 differs from page 1" x x
    # No filename: detection must fall back to sniffing.
    for f in report.docx deck.pptx report.doc sheet.xls deck.ppt page.html invoice.png; do
      c=$(curl -sS -m 300 --data-binary "@$FIX/$f" -o "$t/n" -D "$t/nh" -w '%{http_code}' "$HOST/upload")
      src=$(grep -i '^x-source-type:' "$t/nh" | tr -d '\r' | cut -d' ' -f2)
      check "no filename: $f → $src" 200 "$c"
    done
    rm -rf "$t"
    echo "ok=$ok fail=$bad"; [[ $bad -eq 0 ]]
    ;;
  *) sed -n '2,13p' "$0" ;;
esac
