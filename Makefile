run:
	@npm run --silent build --prefix web
	@go run ./cmd/main.go

up:
	docker compose up -d --build --wait
	@url="http://$$(docker compose port engine 8080)"; echo "Software Factory is ready: $$url"; \
	if [ "$$(uname)" = Darwin ]; then open "$$url"; \
	elif command -v xdg-open >/dev/null 2>&1; then xdg-open "$$url" >/dev/null 2>&1 || true; fi

dev:
	@go run ./dev/watch

down:
	docker compose down

fix:
	@go fix ./...

fix-check:
	@go fix -diff ./...

GOLANGCI_LINT_VERSION := v2.14.0

lint:
	@go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

GO_ARCH_LINT_VERSION := v1.15.0

arch:
	@go run github.com/fe3dback/go-arch-lint@$(GO_ARCH_LINT_VERSION) check --output-color=false

arch-check:
	@out="$$(go run github.com/fe3dback/go-arch-lint@$(GO_ARCH_LINT_VERSION) check --output-color=false 2>&1)"; \
	rc=$$?; \
	printf '%s\n' "$$out"; \
	if [ $$rc -ne 0 ]; then exit $$rc; fi; \
	printf '%s\n' "$$out" | grep -Eq '^[[:space:]]*On \| Base: component imports' || { echo 'arch-check: component-imports linter is OFF — refusing to pass'; exit 1; }; \
	printf '%s\n' "$$out" | grep -Eq '^[[:space:]]*On \| Advanced: vendor imports' || { echo 'arch-check: vendor-import lock is OFF (core purity unguarded) — refusing to pass'; exit 1; }



GOVULNCHECK_VERSION := v1.9.0

vuln:
	@go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

TSC := web/node_modules/.bin/tsc

web-check:
	@npm --prefix web run check

plugin-check:
	@$(TSC) -p tools/plugincheck/tsconfig.json
	@if [ -f plugin/.claude-plugin/types/tsconfig.json ]; then \
		$(TSC) -p plugin/tsconfig.json --noEmit && echo 'plugin-check: passes against Claude Code'"'"'s own declarations too'; \
	else \
		echo 'plugin-check: plugin/.claude-plugin/types is absent, so only tools/plugincheck'"'"'s declarations were checked'; \
	fi

AGENTS_VOLUME ?= software-factory_agents

plugin-validate:
	@docker volume inspect $(AGENTS_VOLUME) >/dev/null 2>&1 || { echo "plugin-validate: the volume $(AGENTS_VOLUME) doesn't exist yet; run make up once first, so the engine installs Claude Code into it"; exit 1; }
	@docker run --rm --network none --user 10002 -e HOME=/tmp --tmpfs /tmp \
		-v $(AGENTS_VOLUME):/opt/software-factory/agents:ro -v "$(CURDIR)/plugin:/plugin:ro" \
		--entrypoint /opt/software-factory/agents/bin/claude software-factory:local plugin validate /plugin

CROSS_TARGETS := linux/amd64 linux/arm64 darwin/arm64

cross-build:
	@for t in $(CROSS_TARGETS); do \
		echo "cross-build: $$t"; \
		GOOS=$${t%/*} GOARCH=$${t#*/} CGO_ENABLED=0 go build ./... && \
		GOOS=$${t%/*} GOARCH=$${t#*/} CGO_ENABLED=0 go vet ./... || exit 1; \
	done

ACTIONLINT_IMAGE := docker.io/rhysd/actionlint:1.7.12@sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667
ZIZMOR_IMAGE := ghcr.io/zizmorcore/zizmor:1.32.0@sha256:3014cc8f68f663935f69b3c3e0e41f5ce102b268d97db13db0f62208747f860d
LYCHEE_IMAGE := docker.io/lycheeverse/lychee:0.24.2@sha256:e2d19e57cf6ab037026f20b8e449a1f30d9d7f81eef4194763aab2eab20bd28d
GITLEAKS_IMAGE := ghcr.io/gitleaks/gitleaks:v8.30.1@sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f
SHELLCHECK_IMAGE := docker.io/koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d
MARKDOWNLINT_IMAGE := docker.io/davidanson/markdownlint-cli2:v0.18.1@sha256:173cb697a255a8a985f2c6a83b4f7a8b3c98f4fb382c71c45f1c52e4d4fed63a
TYPOS_VERSION := 1.52.0

no-tests:
	@sh scripts/check-no-tests.sh

files:
	@sh scripts/check-files.sh

review-workflow:
	@sh scripts/check-review-workflow.sh

no-comments:
	@cd scripts/nocomments && go vet . && sh ../files.sh | go run . -root ../..

workflows: review-workflow
	@docker run --rm --network none -v "$(CURDIR):/repo:ro" -w /repo $(ACTIONLINT_IMAGE)
	@if [ -n "$$GH_TOKEN" ]; then net=bridge; offline=; else net=none; offline=--offline; fi; \
	docker run --rm --network "$$net" -e GH_TOKEN -v "$(CURDIR):/repo:ro" -w /repo $(ZIZMOR_IMAGE) \
		--strict-collection --no-progress --color never $$offline .github

shell:
	@sh scripts/files.sh '*.sh' | xargs -0 docker run --rm --network none -v "$(CURDIR):/mnt:ro" -w /mnt $(SHELLCHECK_IMAGE) -s sh -S warning

markdown:
	@sh scripts/files.sh '*.md' | xargs -0 docker run --rm --network none -v "$(CURDIR):/workdir:ro" -w /workdir $(MARKDOWNLINT_IMAGE)

links:
	@sh scripts/files.sh '*.md' | xargs -0 docker run --rm --network none -v "$(CURDIR):/input:ro" -w /input $(LYCHEE_IMAGE) \
		--offline --include-fragments --no-progress

secrets:
	@sh scripts/scan-secrets.sh $(GITLEAKS_IMAGE)

spelling:
	@command -v typos >/dev/null || { echo "spelling: typos isn't installed; install typos $(TYPOS_VERSION) from https://github.com/crate-ci/typos"; exit 1; }
	@typos --version | grep -qx 'typos-cli $(TYPOS_VERSION)' || echo "spelling: CI runs typos $(TYPOS_VERSION); this is $$(typos --version)"
	@sh scripts/files.sh | xargs -0 typos --config .typos.toml

.PHONY: up dev down cross-build no-comments no-tests files review-workflow workflows shell markdown links secrets spelling
