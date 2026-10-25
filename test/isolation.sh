#!/bin/sh
# Untrusted HTML must neither run JavaScript nor fetch anything.
# - Fetches: a probe server on the same Docker network logs every request; each page below points at it,
#   and the log must stay empty (a sanity request from the host proves the log works). Works offline.
# - JavaScript and file://: each page is rendered next to an identical page without the probe,
#   the PNGs must be byte-identical.
# Usage: test/isolation.sh [image]   (default dockette/docshot:latest)
IMAGE=${1:-dockette/docshot:latest}
PORT=39995
PROBE_PORT=39996
NET=docshot-isolation
DIR=
cleanup() { docker rm -f $NET-app $NET-probe >/dev/null 2>&1; docker network rm $NET >/dev/null 2>&1; rm -rf "$DIR"; }
trap cleanup EXIT

cleanup
DIR=$(mktemp -d)
docker network create $NET >/dev/null || exit 1
docker run -d --name $NET-probe --network $NET --network-alias probe -p $PROBE_PORT:8080 busybox \
	sh -c 'mkdir /www && exec httpd -f -vv -p 8080 -h /www' >/dev/null || exit 1
docker run -d --name $NET-app --network $NET -p $PORT:3000 "$IMAGE" >/dev/null || exit 1
i=0
until curl -fsS -m 2 "http://localhost:$PORT/readyz" >/dev/null 2>&1; do
	i=$((i + 1)); [ $i -gt 60 ] && { echo "isolation: docshot did not become ready"; exit 1; }
	sleep 2
done

page() { printf '<!doctype html><html><head><style>body{background:#fff;font:20px sans-serif}</style>%s</head><body><h1>isolation</h1>%s</body></html>' "$1" "$2"; }
render() { curl -sS -m 120 --data-binary @- "http://localhost:$PORT/upload?name=p.html" -o "$DIR/$1.png"; }

fail=0
ok() { echo "isolation: $1 ok"; }
bad() { echo "isolation: $1 FAILED ($2)"; fail=1; }

# Fetches: every resource type, all aimed at the probe server (a private IP).
P=http://probe:8080
page "<script src=\"$P/script\"></script><link rel=\"stylesheet\" href=\"$P/style\"><style>@import \"$P/import\"; h1{background:url($P/css-url)}</style>" \
	"<img src=\"$P/img\"><iframe src=\"$P/iframe\"></iframe><object data=\"$P/object\"></object><script>new Image().src=\"$P/beacon\";fetch(\"$P/fetch\")</script>" | render fetch
curl -fsS -m 5 "http://localhost:$PROBE_PORT/sanity" >/dev/null 2>&1
log=$(docker logs $NET-probe 2>&1)
echo "$log" | grep -q sanity || bad probe-server "request log not working"
for r in script style import css-url img iframe object beacon fetch; do
	if echo "$log" | grep -q "/$r"; then bad "fetch-$r" "docshot requested $P/$r"; else ok "fetch-$r"; fi
done

# JavaScript, public fetch (only meaningful online) and file://: output must not change.
probe() { # name, probe head, probe body, control head, control body
	page "$2" "$3" | render "$1-probe"
	page "$4" "$5" | render "$1-control"
	if [ -s "$DIR/$1-control.png" ] && cmp -s "$DIR/$1-probe.png" "$DIR/$1-control.png"; then ok "$1"; else bad "$1" "probe changed the rendered page"; fi
}
probe script '' '<script>document.body.style.background="#f00"</script>' '' ''
probe remote-image '' '<img src="https://www.google.com/images/branding/googlelogo/1x/googlelogo_color_272x92dp.png" width="272" height="92">' '' '<img src="data:," width="272" height="92">'
probe file-iframe '' '<iframe src="file:///etc/passwd" width="400" height="100"></iframe>' '' '<iframe src="file:///nonexistent" width="400" height="100"></iframe>'

exit $fail
