# Manual installs: the binary must be named like its plugin folder (plugins/zoraxy-home/zoraxy-home).
NAME := zoraxy-home
# Release assets follow the plugin store's naming: last segment of the plugin ID + _os_arch
ASSET := home
RELEASE_TARGETS := linux/amd64 linux/386 linux/arm linux/arm64 linux/mipsle linux/riscv64 windows/amd64

.PHONY: build linux-amd64 linux-arm64 release introspect test clean

build:
	go build -o $(NAME) .

linux-amd64:
	mkdir -p dist/linux-amd64/$(NAME)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/linux-amd64/$(NAME)/$(NAME) .

linux-arm64:
	mkdir -p dist/linux-arm64/$(NAME)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/linux-arm64/$(NAME)/$(NAME) .

release:
	rm -rf dist/release && mkdir -p dist/release
	@for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $${os}_$${arch}"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w" -o dist/release/$(ASSET)_$${os}_$${arch}$$ext . || exit 1; \
	done
	cd dist/release && shasum -a 256 $(ASSET)_* > SHA256SUMS

# The plugin store reads .introspect from the repo root
introspect: build
	./$(NAME) -introspect > .introspect

test:
	go vet ./...
	go test ./...

clean:
	rm -rf dist $(NAME)
