PHONY: source generate

source:
	set -a && source ../asagumo/.env && set +a

generate:
	go run cmd/gen/main.go