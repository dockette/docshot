# docshot — any document → one page as an image, over HTTP
# Override COMPOSE if your user needs sudo: make up COMPOSE="sudo docker compose"
COMPOSE ?= docker compose
DOCKER_IMAGE ?= dockette/docshot:latest

.DEFAULT_GOAL := up
.PHONY: test-formats build run up down restart logs ps validate test test-params test-all formats test-unit lint smoke

build:            ## Build the public image (docker buildx)
	docker buildx build -t ${DOCKER_IMAGE} .

run:              ## Run the public image on :3000
	docker run -it --rm -p 3000:3000 ${DOCKER_IMAGE}

# The code is Linux-only (prctl, process groups), so Go runs in a container on any host.
GO_DOCKER ?= docker run --rm -v $(CURDIR):/src -w /src -v docshot-gomod:/go/pkg/mod -v docshot-gocache:/root/.cache golang:1.27-trixie

test-unit:        ## Go vet + unit tests (in a golang container)
	$(GO_DOCKER) sh -c 'go vet -tags nodynamic ./... && go test -tags nodynamic -race ./...'

lint:             ## gofmt, staticcheck, govulncheck, shellcheck, hadolint
	$(GO_DOCKER) sh -c 'test -z "$$(gofmt -l .)" && go run honnef.co/go/tools/cmd/staticcheck@452d5bb86b45 -tags nodynamic ./... && go run golang.org/x/vuln/cmd/govulncheck@latest -tags nodynamic ./...'
	docker run --rm -v $(CURDIR):/mnt -w /mnt koalaman/shellcheck:stable -S warning docshot.sh test/formats/gen.sh
	docker run --rm -i -v $(CURDIR)/.hadolint.yaml:/.config/hadolint.yaml:ro hadolint/hadolint < Dockerfile

smoke:            ## Start the public image, wait for /readyz, render Markdown to PNG
	@docker rm -f docshot-smoke >/dev/null 2>&1; docker run -d --name docshot-smoke -p 39994:3000 ${DOCKER_IMAGE} >/dev/null; \
	for i in $$(seq 1 60); do curl -fsS -m 2 http://localhost:39994/readyz >/dev/null 2>&1 && break; sleep 2; done; \
	ct=$$(printf '# docshot\n\nsmoke test\n' | curl -sS -m 120 -o /dev/null -w '%{http_code} %{content_type}' --data-binary @- 'http://localhost:39994/upload?name=smoke.md'); \
	docker rm -f docshot-smoke >/dev/null; echo "smoke: $$ct"; [ "$$ct" = "200 image/png" ]

up:               ## Build (if needed) + start docshot (Gotenberg inside)
	$(COMPOSE) up -d --build
	@echo "[docshot] REST -> http://localhost:9994  (POST /upload, GET /render, /info, /readyz, /metrics)"

down:             ## Stop
	$(COMPOSE) down

restart:          ## Restart
	$(COMPOSE) restart

logs:             ## Tail logs
	$(COMPOSE) logs -f --tail=100

ps:               ## Container status
	$(COMPOSE) ps

validate:         ## Health-check (docshot + Gotenberg)
	@$(COMPOSE) ps
	@./docshot.sh health

formats:          ## Supported formats and their routes
	@./docshot.sh formats

test-all:         ## Every /srv/tika/fixtures file, sentinel-checked via Tika OCR
	@./docshot.sh test-all

test-params:      ## page / format / width / error codes / sniffing without filename
	@./docshot.sh test-params

test: test-params test-all

test-formats:     ## Every extension: generate/download samples, render, OCR-check (slow)
	@OUT=$${OUT:-/tmp/docshot-formats}; FIX=$${FIXTURES:-$(CURDIR)/../tika/fixtures}; \
	rm -rf $$OUT && mkdir -p $$OUT/gen/in $$OUT/gen/out $$OUT/corpus $$OUT/png && chmod -R 777 $$OUT && \
	cp $$FIX/report.docx $$FIX/sheet.xlsx $$FIX/deck.pptx $$FIX/invoice.png $$FIX/sample.eml $$FIX/notes.epub $$FIX/sample.tar.gz $$FIX/sample.jar $$FIX/page.html $$OUT/gen/in/ && \
	cp test/formats/gen.sh $$OUT/gen/ && docker run --rm --network none --entrypoint sh -v $$OUT/gen:/w $${IMAGE:-docshot:0.1.0} /w/gen.sh >/dev/null 2>&1; \
	grep -v '^#' test/formats/corpus.txt | while read u n; do [ -n "$$u" ] && curl -sfL --max-time 120 -o $$OUT/corpus/$$n "$$u"; done; \
	python3 test/formats/fixtures.py $$OUT && python3 test/formats/run.py $$OUT
