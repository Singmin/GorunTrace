FROM node:22-alpine AS dashboard
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY sdk/ sdk/
COPY pkg/ pkg/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -o /out/goruntrace ./cmd/server && CGO_ENABLED=0 go build -trimpath -o /out/goruntrace-mcp ./cmd/mcp

FROM backend AS test
RUN apk add --no-cache gcc musl-dev
RUN go vet ./... && go test -race ./... -timeout 90s

FROM alpine:3.22
RUN addgroup -g 10001 goruntrace && adduser -D -H -u 10001 -G goruntrace goruntrace && mkdir -p /app/data && chown goruntrace:goruntrace /app/data && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=backend /out/ /usr/local/bin/
COPY --from=dashboard /src/web/dist/ /app/web/dist/
USER goruntrace
EXPOSE 8080
VOLUME ["/app/data"]
ENTRYPOINT ["goruntrace"]
CMD ["-addr", "0.0.0.0:8080"]
