# Build stage
FROM node:20-alpine AS builder

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
FROM nginx:alpine

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

# Create non-root user
RUN adduser -D -g '' appuser && \
    chown -R appuser:appuser /var/cache/nginx && \
    chown -R appuser:appuser /var/log/nginx && \
    chown -R appuser:appuser /etc/nginx/conf.d && \
    touch /var/run/nginx.pid && \
    chown -R appuser:appuser /var/run/nginx.pid

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
