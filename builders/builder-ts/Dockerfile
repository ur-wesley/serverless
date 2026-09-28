# Generic TypeScript builder: compiles examples/<fn> into a /app/handler binary.
# Build a function with:
#   docker build -f builders/builder-ts/Dockerfile -t hello-ts:latest examples/hello-ts
# Expects build context = function dir containing package.json + src/.
FROM oven/bun:1 AS build
WORKDIR /src
COPY package.json bun.lock* ./
RUN bun install --frozen-lockfile 2>/dev/null || bun install || true
COPY src/ ./src/
RUN bun build --compile ./src/index.ts --outfile /app/handler

FROM debian:bookworm-slim
RUN useradd -m handler || true
COPY --from=build /app/handler /app/handler
EXPOSE 8080
ENTRYPOINT ["/app/handler"]
