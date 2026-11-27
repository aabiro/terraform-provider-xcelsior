default: build

build:
	go build ./...

install:
	go install .

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt: the files above need formatting" && exit 1)
	go vet ./...

test:
	go test ./... -count=1 -timeout 10m

# Acceptance tests create billed resources on a local or staging API, never
# production; see README.md, "Acceptance tests".
testacc:
	TF_ACC=1 go test ./internal/provider/ -run '^TestAcc' -v -count=1 -timeout 60m

docs:
	go generate ./...

.PHONY: default build install lint test testacc docs
