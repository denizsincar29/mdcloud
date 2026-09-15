# mdcloud — сборка, тесты, запуск для разработки.

GO ?= go
BIN ?= mdcloud

.PHONY: build test vet fmt run clean

build:            ## собрать бинарь
	$(GO) build -o $(BIN) .

test:             ## прогнать тесты (sqlite в памяти, Postgres не нужен)
	$(GO) test ./...

vet:              ## статический анализ
	$(GO) vet ./...

fmt:              ## форматирование
	gofmt -w .

run: build        ## локальный запуск со статикой из web/
	MDCLOUD_STATIC_DIR=$(CURDIR)/web ./$(BIN)

clean:
	rm -f $(BIN)
