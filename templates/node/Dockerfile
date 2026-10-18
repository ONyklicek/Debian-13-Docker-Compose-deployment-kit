# syntax=docker/dockerfile:1.7
# Generic Node.js template: installs deps, runs "build" if present, starts with "npm start".
# The app must listen on $PORT (default 3000). Build-time env is mounted as .env.production.local.
ARG NODE_VERSION=22
FROM node:${NODE_VERSION}-bookworm-slim AS build
WORKDIR /app
COPY . .
RUN --mount=type=secret,id=buildenv,target=/app/.env.production.local \
    if [ -f pnpm-lock.yaml ]; then corepack enable && pnpm install --frozen-lockfile && pnpm run --if-present build && pnpm prune --prod; \
    elif [ -f yarn.lock ]; then corepack enable && yarn install --frozen-lockfile && (yarn run build || true) && yarn install --production --frozen-lockfile; \
    else npm ci && npm run build --if-present && npm prune --omit=dev; fi

FROM node:${NODE_VERSION}-bookworm-slim
ENV NODE_ENV=production PORT=3000 HOSTNAME=0.0.0.0
WORKDIR /app
COPY --from=build --chown=node:node /app /app
USER node
EXPOSE 3000
CMD ["npm", "start"]
