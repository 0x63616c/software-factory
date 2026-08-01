# Multi-stage build for the software-factory console (React/Vite static bundle).
# Stage 1 builds the bundle with bun; stage 2 serves it via nginx, proxying
# /api to the factory API's in-cluster Service (nginx.conf).

FROM oven/bun:1.2-alpine AS builder

WORKDIR /app

COPY web/package.json web/bun.lock ./web/
RUN bun install --cwd web --frozen-lockfile --ignore-scripts
COPY tsconfig.json ./tsconfig.json
COPY web ./web
RUN bun run --cwd web build


# Stage 2: nginx serves the static bundle and proxies /api to the api.
FROM nginxinc/nginx-unprivileged:1.27-alpine AS runtime

RUN rm /etc/nginx/conf.d/default.conf

COPY web/nginx.conf /etc/nginx/conf.d/app.conf

COPY --from=builder /app/web/dist /usr/share/nginx/html

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
