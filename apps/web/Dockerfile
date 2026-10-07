# Base images are pinned by digest; Dependabot (.github/dependabot.yml) bumps them.
# Build stage
FROM node:24.21.0-alpine3.24@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1 AS builder

WORKDIR /app

# Copy package files
COPY package*.json ./
RUN npm ci

# Copy source code
COPY . .

# Build for production
ARG VITE_API_URL
ENV VITE_API_URL=${VITE_API_URL}
RUN npm run build

# Production stage
FROM nginx:1.31.0-alpine@sha256:2f07d83bf561b506400dc183b1b2003803e39efbd22451f848adaba14d28c7c7

# nginx config: the image's entrypoint renders /etc/nginx/templates/*.template
# into /etc/nginx/conf.d at start. Only MAILAT_* variables are substituted.
COPY docker/templates/ /etc/nginx/templates/
COPY docker/snippets/ /etc/nginx/snippets/
# Extra connect-src origins for the SPA CSP, e.g. "https://api.example.com"
# when VITE_API_URL points at a separate API domain.
ENV MAILAT_CSP_CONNECT_SRC=""
ENV NGINX_ENVSUBST_FILTER="^MAILAT_"

# Copy built files
COPY --from=builder /app/dist /usr/share/nginx/html

# The nginx master runs as root so it can bind port 80 under any runtime;
# the image's nginx.conf ("user nginx;") runs every worker that handles
# requests as the unprivileged nginx user. A fully non-root image waits until
# the live stack's runtime options (network mode, dropped capabilities) are known.
# Until then the image keeps the older appuser account and its writable nginx
# paths, so a runtime that sets `user: appuser` still starts. There is
# deliberately no USER line.
RUN adduser -D -g '' appuser && \
    chown -R appuser:appuser /var/cache/nginx && \
    chown -R appuser:appuser /var/log/nginx && \
    chown -R appuser:appuser /etc/nginx/conf.d && \
    touch /var/run/nginx.pid && \
    chown -R appuser:appuser /var/run/nginx.pid

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
