#!/bin/sh
# Untrusted HTML must neither run JavaScript nor fetch anything (CDN scripts/styles, public images, file://).
# Each probe page is rendered next to an identical page without the probe: the PNGs must be byte-identical.
# Usage: test/isolation.sh [image]   (default dockette/docshot:latest)
IMAGE=${1:-dockette/docshot:latest}
PORT=39995
NAME=docshot-isolation
DIR=$(mktemp -d)
trap 'docker rm -f $NAME >/dev/null 2>&1; rm -rf "$DIR"' EXIT

docker rm -f $NAME >/dev/null 2>&1
docker run -d --name $NAME -p $PORT:3000 "$IMAGE" >/dev/null || exit 1
i=0
until curl -fsS -m 2 "http://localhost:$PORT/readyz" >/dev/null 2>&1; do
	i=$((i + 1)); [ $i -gt 60 ] && { echo "isolation: docshot did not become ready"; exit 1; }
	sleep 2
done

page() { printf '<!doctype html><html><head><style>body{background:#fff;font:20px sans-serif}</style>%s</head><body><h1>isolation</h1>%s</body></html>' "$1" "$2"; }
render() { curl -sS -m 120 --data-binary @- "http://localhost:$PORT/upload?name=p.html" -o "$DIR/$1.png"; }

fail=0
probe() { # name, probe head, probe body, control head, control body
	page "$2" "$3" | render "$1-probe"
	page "$4" "$5" | render "$1-control"
	if [ -s "$DIR/$1-control.png" ] && cmp -s "$DIR/$1-probe.png" "$DIR/$1-control.png"; then
		echo "isolation: $1 ok"
	else
		echo "isolation: $1 FAILED (probe changed the rendered page)"; fail=1
	fi
}

probe script '' '<script>document.body.style.background="#f00"</script>' '' ''
probe cdn-script '<script src="https://cdnjs.cloudflare.com/ajax/libs/jquery/3.7.1/jquery.min.js"></script>' '' '' ''
probe cdn-style '<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/bootstrap/5.3.3/css/bootstrap.min.css">' '' '' ''
probe remote-image '' '<img src="https://www.google.com/images/branding/googlelogo/1x/googlelogo_color_272x92dp.png" width="272" height="92">' '' '<img src="data:," width="272" height="92">'
probe file-iframe '' '<iframe src="file:///etc/passwd" width="400" height="100"></iframe>' '' '<iframe src="file:///nonexistent" width="400" height="100"></iframe>'

exit $fail
