# syntax=docker/dockerfile:1.7
# Generic PHP template (Laravel, Symfony, plain PHP): nginx + PHP-FPM in one image, port 8080.
# Build context is the application repository. Build-time env (BUILD_ARGS) is mounted as
# .env.production.local for the frontend build only, so Vite/Next pick it up.
ARG PHP_VERSION=8.4
ARG NODE_VERSION=22

FROM node:${NODE_VERSION}-bookworm-slim AS frontend
WORKDIR /src
COPY . .
RUN --mount=type=secret,id=buildenv,target=/src/.env.production.local \
    if [ -f package-lock.json ]; then npm ci && npm run build --if-present; \
    elif [ -f pnpm-lock.yaml ]; then corepack enable && pnpm install --frozen-lockfile && pnpm run build; \
    elif [ -f yarn.lock ]; then corepack enable && yarn install --frozen-lockfile && yarn build; \
    else mkdir -p public; fi

FROM composer:2 AS vendor
WORKDIR /src
COPY . .
RUN if [ -f composer.json ]; then \
      composer install --no-dev --prefer-dist --no-interaction --no-progress --optimize-autoloader --no-scripts --ignore-platform-reqs; \
    fi

FROM php:${PHP_VERSION}-fpm-bookworm AS runtime
ARG DOCROOT=public
# Extra extensions via TEMPLATE_ARGS="PHP_EXTENSIONS=soap imagick" (installed with install-php-extensions).
ARG PHP_EXTENSIONS=""
ARG APT_PACKAGES=""
ENV DEBIAN_FRONTEND=noninteractive
COPY --from=mlocati/php-extension-installer:2 /usr/bin/install-php-extensions /usr/local/bin/
RUN apt-get update && apt-get install -y --no-install-recommends nginx supervisor curl ca-certificates ${APT_PACKAGES} \
    && install-php-extensions pdo_mysql pdo_pgsql mysqli intl bcmath pcntl opcache gd zip exif redis ${PHP_EXTENSIONS} \
    && apt-get clean && rm -rf /var/lib/apt/lists/* \
    && mv "$PHP_INI_DIR/php.ini-production" "$PHP_INI_DIR/php.ini" \
    && printf 'expose_php=Off\nmemory_limit=512M\nupload_max_filesize=64M\npost_max_size=64M\nopcache.validate_timestamps=0\nopcache.memory_consumption=192\n' > "$PHP_INI_DIR/conf.d/zz-dkit.ini" \
    && printf '[www]\nclear_env = no\npm = dynamic\npm.max_children = 20\n' > /usr/local/etc/php-fpm.d/zz-dkit.conf
WORKDIR /var/www/html
COPY --from=vendor --chown=www-data:www-data /src /var/www/html
COPY --from=frontend --chown=www-data:www-data /src/public /var/www/html/public
RUN cat > /etc/nginx/sites-available/default <<NGINX
server {
    listen 8080 default_server;
    server_name _;
    root /var/www/html/${DOCROOT};
    index index.php index.html;
    server_tokens off;
    client_max_body_size 64m;
    set_real_ip_from 172.16.0.0/12;
    set_real_ip_from 10.0.0.0/8;
    set_real_ip_from 192.168.0.0/16;
    real_ip_header X-Forwarded-For;
    location / { try_files \$uri \$uri/ /index.php?\$query_string; }
    location ~ \.php$ {
        try_files \$uri =404;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME \$document_root\$fastcgi_script_name;
        fastcgi_param HTTP_PROXY "";
        fastcgi_pass 127.0.0.1:9000;
        fastcgi_read_timeout 300s;
    }
    location ~ /\.(?!well-known).* { deny all; }
}
NGINX
RUN cat > /etc/supervisor/conf.d/app.conf <<'SUPERVISOR'
[supervisord]
nodaemon=true
user=root
logfile=/dev/null
pidfile=/run/supervisord.pid
[program:php-fpm]
command=/usr/local/sbin/php-fpm --nodaemonize
autorestart=true
stopsignal=QUIT
stdout_logfile=/dev/fd/1
stdout_logfile_maxbytes=0
stderr_logfile=/dev/fd/2
stderr_logfile_maxbytes=0
[program:nginx]
command=/usr/sbin/nginx -g "daemon off;"
autorestart=true
stopsignal=QUIT
stdout_logfile=/dev/fd/1
stdout_logfile_maxbytes=0
stderr_logfile=/dev/fd/2
stderr_logfile_maxbytes=0
SUPERVISOR
RUN ln -sf /dev/stdout /var/log/nginx/access.log && ln -sf /dev/stderr /var/log/nginx/error.log \
    && for d in storage bootstrap/cache var; do [ ! -d "$d" ] || chown -R www-data:www-data "$d"; done
EXPOSE 8080
# Workers run as www-data via WORKER_*_CMD; the web process keeps the supervisor default.
CMD ["/usr/bin/supervisord", "-n", "-c", "/etc/supervisor/supervisord.conf"]
