FROM golang:1.23.11-alpine AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM source AS build
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -o /out/outbox ./cmd/outbox

FROM source AS tests
RUN apk add --no-cache gcc musl-dev
ENV CGO_ENABLED=1
CMD ["go", "test", "-count=1", "-timeout=120s", "./..."]

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates && \
    addgroup -g 10001 app && adduser -D -u 10001 -G app app && \
    mkdir -p /app/data/storage && chown -R app:app /app
WORKDIR /app
COPY --from=build /out/server /out/worker /out/migrate /out/outbox /app/
COPY api/openapi.yml /app/api/openapi.yml
USER 10001:10001
EXPOSE 8080
CMD ["/app/server"]
