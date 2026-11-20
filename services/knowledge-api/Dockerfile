# Build context: repository root (see deploy/docker-compose.yml).
FROM golang:1.27-alpine AS build
WORKDIR /src/services/knowledge-api
COPY services/knowledge-api/go.mod services/knowledge-api/go.sum ./
RUN go mod download
COPY services/knowledge-api/ ./
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/kn-api ./cmd/kn-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kn-api /kn-api
USER nonroot:nonroot
EXPOSE 8787
ENTRYPOINT ["/kn-api"]
