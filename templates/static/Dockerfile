# syntax=docker/dockerfile:1.7
# Static site / SPA: optional npm build, served by unprivileged nginx on port 8080.
# TEMPLATE_ARGS: STATIC_DIR=dist (build output; "." for plain HTML), SPA=1 (fallback to index.html).
ARG NODE_VERSION=22
FROM node:${NODE_VERSION}-bookworm-slim AS build
ARG STATIC_DIR=dist
WORKDIR /src
COPY . .
RUN --mount=type=secret,id=buildenv,target=/src/.env.production.local \
    if [ -f package.json ]; then \
      if [ -f pnpm-lock.yaml ]; then corepack enable && pnpm install --frozen-lockfile && pnpm run build; \
      elif [ -f yarn.lock ]; then corepack enable && yarn install --frozen-lockfile && yarn build; \
      else npm ci && npm run build; fi; \
    fi \
    && mkdir -p /out && cp -a "${STATIC_DIR}/." /out/ \
    && rm -rf /out/node_modules /out/.git

FROM nginxinc/nginx-unprivileged:1.28-alpine
USER root
ARG SPA=0
COPY --from=build /out /usr/share/nginx/html
RUN if [ "$SPA" = "1" ]; then fallback='/index.html'; else fallback='=404'; fi \
    && printf 'server {\n  listen 8080;\n  server_name _;\n  server_tokens off;\n  root /usr/share/nginx/html;\n  location / { try_files $uri $uri/ %s; }\n  location ~ /\\.(?!well-known) { deny all; }\n}\n' "$fallback" > /etc/nginx/conf.d/default.conf
USER 101
EXPOSE 8080
