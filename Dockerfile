# syntax=docker/dockerfile:1
FROM node:24.21.0-bookworm-slim AS frontend
ARG NEXT_PUBLIC_TURNSTILE_SITE_KEY
ENV NEXT_PUBLIC_TURNSTILE_SITE_KEY=$NEXT_PUBLIC_TURNSTILE_SITE_KEY
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm install -g pnpm@11.20.0
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/next.config.ts web/tsconfig.json web/postcss.config.mjs web/eslint.config.mjs web/components.json ./
COPY web/src/ ./src/
RUN test -n "$NEXT_PUBLIC_TURNSTILE_SITE_KEY" && pnpm lint && pnpm build && pnpm typecheck

FROM golang:1.26.5-bookworm AS backend
RUN apt-get update && apt-get install -y --no-install-recommends tcl libssl-dev sqlite3 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /tmp/sqlcipher
ADD https://codeload.github.com/sqlcipher/sqlcipher/tar.gz/refs/tags/v4.19.0 /tmp/sqlcipher.tar.gz
RUN echo "7075f96cbabe45b4ecfc2e6b1745a625f856f695b0827a5506ce9ed85b906aa0  /tmp/sqlcipher.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/sqlcipher.tar.gz --strip-components=1 \
    && ./configure --prefix=/opt/sqlcipher --with-tempstore=yes --disable-static \
       CFLAGS="-O2 -DSQLITE_HAS_CODEC -DSQLITE_EXTRA_INIT=sqlcipher_extra_init -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown" \
       LDFLAGS="-lcrypto" \
    && make -j2 && make install
RUN go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
WORKDIR /src
COPY backend/ backend/
COPY scripts/go.sh scripts/go.sh
RUN cd backend && sqlc generate
RUN ./scripts/go.sh test ./... \
    && ./scripts/go.sh build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libssl3 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 budget && useradd --uid 10001 --gid 10001 --no-create-home budget \
    && mkdir -p /data /app/web && chown 10001:10001 /data
COPY --from=backend /opt/sqlcipher/lib/libsqlite3.so* /usr/local/lib/
RUN ldconfig
COPY --from=backend /app/server /app/server
COPY --from=frontend /src/web/out/ /app/web/
ENV APP_LISTEN_ADDR=0.0.0.0:8080 \
    APP_DATABASE_PATH=/data/budget.db \
    APP_DATABASE_KEY_FILE=/run/secrets/database_key \
    APP_WEB_DIR=/app/web
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/server", "healthcheck"]
ENTRYPOINT ["/app/server"]
