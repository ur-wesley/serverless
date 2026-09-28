# Go builder: compiles <func-dir> (go.mod + handler src) into /app/handler.
#   docker build -f builders/builder-go/Dockerfile -t hello-go:latest examples/hello-go
# Handler must serve GET /healthz + POST /invoke on $PORT (see examples/hello-go).
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /app/handler .

FROM debian:bookworm-slim
RUN useradd -m handler || true
COPY --from=build /app/handler /app/handler
EXPOSE 8080
ENTRYPOINT ["/app/handler"]
