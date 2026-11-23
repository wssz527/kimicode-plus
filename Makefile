.PHONY: build test check crossbuild run
build:
	sh scripts/build.sh

test:
	go test ./cmd/... ./internal/... ./plugins/...

check:
	go vet ./cmd/... ./internal/... ./plugins/...
	cd web && npm ci && npm run typecheck && npm test
	python3 scripts/test_package.py && python3 scripts/test_sync_cnb_release.py

crossbuild:
	sh scripts/crossbuild.sh

run:
	go run ./cmd/enhanced-kimi --data-dir ./work/development
