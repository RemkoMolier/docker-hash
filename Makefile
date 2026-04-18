.PHONY: setup lint fmt check

setup:
	mise install && lefthook install

lint:
	lefthook run pre-commit --all-files

fmt:
	gofmt -w . && golangci-lint run --fix ./...

check: lint
	go test ./... -race -count=1
