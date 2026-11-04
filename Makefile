.PHONY: build test install bench report

build:
	go build -o bin/detour ./cmd/detour

test:
	go vet ./...
	go test ./...

install:
	go install ./cmd/detour

# make bench REPS=3 MODEL=sonnet ONLY=run-hooks
REPS ?= 3
MODEL ?= sonnet
ONLY ?=
bench: build
	./bin/detour bench run -reps $(REPS) -model $(MODEL) $(if $(ONLY),-only $(ONLY))

report:
	@./bin/detour bench report $$(ls -d bench/results/*/ | tail -1)results.jsonl
