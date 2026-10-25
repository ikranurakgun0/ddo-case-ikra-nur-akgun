# --- derleme aşaması ---
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . .
# go.sum repoda yoksa (ilk kurulum) oluşturulur; varsa dokunulmaz.
RUN [ -f go.sum ] || go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/simulator ./cmd/simulator \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/verify ./cmd/verify

# --- çalışma aşaması: küçük imaj, root olmayan kullanıcı ---
# Tek imajda üç ikili: api (varsayılan), simulator (veri üretici), verify (doğrulama).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/api"]
CMD ["serve"]
