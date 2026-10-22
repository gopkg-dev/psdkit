GO ?= go
GOLANGCI_LINT_VERSION := v2.15.0
GOLANGCI_LINT := $(CURDIR)/.tools/golangci-lint

.PHONY: test race vet lint fmt tools wasm web web-test docs site site-preview repository-check check clean

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

$(GOLANGCI_LINT):
	GOBIN=$(CURDIR)/.tools $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

tools: $(GOLANGCI_LINT)

lint: tools
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) fmt --diff

fmt: tools
	$(GOLANGCI_LINT) fmt

wasm:
	$(GO) run ./cmd/psdkit-web-build

web-test:
	npm --prefix web/demo test

web: wasm
	npm --prefix web/demo ci --no-audit --no-fund
	npm --prefix web/demo run build

SITE_DIR ?= _site
SITE_ADDR ?= 127.0.0.1:8301
SITE_BASE_PATH ?= /psdkit

docs:
	BUNDLE_GEMFILE=$(CURDIR)/docs/Gemfile bundle exec jekyll build --source docs --destination $(SITE_DIR) --baseurl "$(SITE_BASE_PATH)"

# 与 GitHub Pages 工作流相同的组装方式：首页与文档在站点根路径，Playground 在 demo/。
site: docs web
	rm -rf $(SITE_DIR)/demo
	cp -R web/demo/dist $(SITE_DIR)/demo
	touch $(SITE_DIR)/.nojekyll

site-preview:
	$(GO) run ./cmd/psdkit-web-server -dir $(SITE_DIR) -base $(SITE_BASE_PATH)/ -addr $(SITE_ADDR)

repository-check:
	node scripts/check-repository.mjs

check: test vet lint web-test repository-check

clean:
	rm -rf bin site _site web/demo/dist web/demo/public/wasm web/demo/public/samples
