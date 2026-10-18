# Production-oriented baseline for conventional Laravel apps.
# Build context is the checked-out Laravel repository; this Dockerfile is supplied by deployment kit.
ARG PHP_VERSION=8.4
FROM node:22-bookworm-slim AS frontend
WORKDIR /src
COPY . .
ARG VITE_REVERB_APP_KEY
ARG VITE_REVERB_HOST
ARG VITE_REVERB_PORT=443
ARG VITE_REVERB_SCHEME=https
ENV VITE_REVERB_APP_KEY=${VITE_REVERB_APP_KEY} \
    VITE_REVERB_HOST=${VITE_REVERB_HOST} \
    VITE_REVERB_PORT=${VITE_REVERB_PORT} \
    VITE_REVERB_SCHEME=${VITE_REVERB_SCHEME}
RUN if [ -f package-lock.json ]; then npm ci && npm run build; fi

FROM composer:2 AS vendor
WORKDIR /src
COPY composer.json composer.lock ./
RUN composer install --no-dev --prefer-dist --no-interaction --no-progress --optimize-autoloader --no-scripts
COPY . .
RUN composer dump-autoload --no-dev --classmap-authoritative --no-scripts

FROM php:${PHP_VERSION}-fpm-bookworm AS runtime
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    nginx supervisor curl ca-certificates git unzip libicu-dev libzip-dev libpng-dev \
    libjpeg62-turbo-dev libfreetype6-dev libonig-dev libxml2-dev \
    && docker-php-ext-configure gd --with-freetype --with-jpeg \
    && docker-php-ext-install -j"$(nproc)" pdo_mysql intl bcmath pcntl opcache gd zip exif \
    && apt-get clean && rm -rf /var/lib/apt/lists/*
WORKDIR /var/www/html
COPY --from=vendor --chown=www-data:www-data /src /var/www/html
# Copy the full public tree so this also works when the app has no Vite build output.
COPY --from=frontend --chown=www-data:www-data /src/public /var/www/html/public
RUN cat > /etc/nginx/sites-available/default <<'NGINX'
server {
    listen 8080 default_server;
    server_name _;
    root /var/www/html/public;
    index index.php;
    server_tokens off;
    client_max_body_size 32m;
    location / { try_files $uri $uri/ /index.php?$query_string; }
    location ~ \.php$ {
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param HTTP_PROXY "";
        fastcgi_pass 127.0.0.1:9000;
        fastcgi_read_timeout 120s;
    }
    location ~ /\.(?!well-known).* { deny all; }
}
NGINX
RUN cat > /etc/supervisor/conf.d/supervisord.conf <<'SUPERVISOR'
[supervisord]
nodaemon=true
user=root
logfile=/dev/null
pidfile=/run/supervisord.pid
[program:php-fpm]
command=/usr/local/sbin/php-fpm --nodaemonize
user=root
autostart=true
autorestart=true
priority=10
stopsignal=QUIT
stopwaitsecs=30
stdout_logfile=/dev/fd/1
stdout_logfile_maxbytes=0
stderr_logfile=/dev/fd/2
stderr_logfile_maxbytes=0
[program:nginx]
command=/usr/sbin/nginx -g "daemon off;"
user=root
autostart=true
autorestart=true
priority=20
stopsignal=QUIT
stopwaitsecs=30
stdout_logfile=/dev/fd/1
stdout_logfile_maxbytes=0
stderr_logfile=/dev/fd/2
stderr_logfile_maxbytes=0
SUPERVISOR
RUN mkdir -p /run/php /var/log/supervisor \
    && chown -R www-data:www-data storage bootstrap/cache \
    && php -v && php -m
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 CMD curl -fsS http://127.0.0.1:8080/up || exit 1
CMD ["/usr/bin/supervisord", "-n", "-c", "/etc/supervisor/supervisord.conf"]
