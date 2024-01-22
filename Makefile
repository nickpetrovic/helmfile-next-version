BIN := helmfile-next-version

build:
	@go build -o $(BIN) main.go

install:
	@make build
	@mv $(BIN) ${HOME}/.local/bin/$(BIN)
