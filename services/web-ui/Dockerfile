# Build context: repository root (see deploy/docker-compose.yml).
FROM node:22-alpine AS web
WORKDIR /src/services/web-ui/web
COPY services/web-ui/web/package.json services/web-ui/web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY services/web-ui/web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src/services/web-ui
COPY services/web-ui/go.mod ./
RUN go mod download
COPY services/web-ui/ ./
COPY --from=web /src/services/web-ui/internal/static/dist/ ./internal/static/dist/
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/kn-ui ./cmd/kn-ui

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kn-ui /kn-ui
USER nonroot:nonroot
EXPOSE 8790
ENTRYPOINT ["/kn-ui"]
