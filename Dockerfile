# --- derleme aşaması ---
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . .
# go.sum repoda yoksa (ilk kurulum) oluşturulur; varsa dokunulmaz.
RUN [ -f go.sum ] || go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# --- çalışma aşaması: küçük imaj, root olmayan kullanıcı ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /app/api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/api"]
CMD ["serve"]
