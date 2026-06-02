# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Regenerate Templ code, then build a fully static, self-contained binary.
# Static assets (CSS/JS/vendored libs) and migrations are embedded via go:embed,
# so the runtime image needs nothing but the binary.
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020
RUN templ generate
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/karots-pos ./cmd/server

# ---- runtime stage ----
FROM alpine:3.20
WORKDIR /app
RUN adduser -D -u 10001 app
COPY --from=build /out/karots-pos /app/karots-pos
USER app
EXPOSE 3000
ENTRYPOINT ["/app/karots-pos"]
