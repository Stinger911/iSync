.PHONY: build test vet install clean

PREFIX ?= $(HOME)/.local
SYSTEMD_USER_DIR ?= $(HOME)/.config/systemd/user

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/isync ./cmd/isync
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/isyncd ./cmd/isyncd

test:
	go test ./... -race -cover

vet:
	go vet ./...

install:
	./scripts/install.sh --prefix "$(PREFIX)" --systemd-dir "$(SYSTEMD_USER_DIR)"

clean:
	rm -rf bin/
