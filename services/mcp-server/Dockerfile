# Build context: repository root. Used only by the optional mcp-http compose profile.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY pkg/kclient/ pkg/kclient/
COPY services/mcp-server/go.mod services/mcp-server/go.sum services/mcp-server/
WORKDIR /src/services/mcp-server
RUN go mod download
COPY services/mcp-server/ ./
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/kn-mcp ./cmd/kn-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kn-mcp /kn-mcp
USER nonroot:nonroot
EXPOSE 8788
ENTRYPOINT ["/kn-mcp"]
