.PHONY: up down logs test tidy

up:            ## Her şeyi derle ve başlat
	docker compose up --build

down:          ## Durdur (veriyi korur)
	docker compose down

logs:
	docker compose logs -f api

test:          ## Tüm testler, gerçek PostgreSQL ile (Docker gerekir)
	docker compose --profile test run --rm test

tidy:          ## go.sum üretir (commit'lemeniz önerilir; tekrarlanabilir derleme için)
	docker run --rm -v "$(CURDIR)":/src -w /src golang:1.23-alpine go mod tidy
