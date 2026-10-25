# Windows'ta make yoksa doğrudan docker compose komutlarını kullanın (TESTING.md'de listelidir).
.PHONY: up down logs test verify reset-demo load lint tidy

up:            ## Her şeyi derle ve başlat (frontend :3000, API :8080)
	docker compose up --build

down:          ## Durdur (veriyi korur)
	docker compose down

logs:
	docker compose logs -f api simulator

test:          ## Birim + entegrasyon testleri, gerçek PostgreSQL ile
	docker compose --profile test run --rm test

verify:        ## Uçtan uca doğrulama: her özellik ve her zorlu görev için PASS/FAIL tablosu
	docker compose --profile verify run --rm verify

reset-demo:    ## Veriyi ilk seed haline döndür (aynı tohum = aynı veri)
	docker compose stop simulator
	docker compose run --rm simulator reset
	docker compose run --rm simulator seed
	docker compose start simulator

load:          ## k6 yük testi (uzun sürer; verify'a dahil değildir)
	docker compose --profile load run --rm k6

lint:
	docker run --rm -v "$(CURDIR)":/src -w /src golangci/golangci-lint:v1.62.2 golangci-lint run ./...

tidy:          ## go.sum üretir (tekrarlanabilir derleme için commit'leyin)
	docker run --rm -v "$(CURDIR)":/src -w /src golang:1.23-alpine go mod tidy
