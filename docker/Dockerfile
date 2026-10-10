FROM golang:1.23.11-alpine@sha256:ddcd26ec6b109c838725a1d93e3bec6d8b9c47f1fdc696b58820c63c70349c9a AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM source AS build
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -o /out/outbox ./cmd/outbox && \
    CGO_ENABLED=0 go build -trimpath -o /out/alertbridge ./cmd/alertbridge

FROM golang:1.23.11-bookworm@sha256:3860392b86f6b41bb9290534038444a3e3e3bb63c499f11f36ff9864fd6f7f58 AS tests
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=1
CMD ["go", "test", "-count=1", "-timeout=120s", "./..."]

FROM alpine:3.22.3@sha256:55ae5d250caebc548793f321534bc6a8ef1d116f334f18f4ada1b2daad3251b2 AS runtime
RUN apk add --no-cache ca-certificates && \
    addgroup -g 10001 app && adduser -D -u 10001 -G app app && \
    mkdir -p /app/data/storage && chown -R app:app /app
WORKDIR /app
COPY --from=build /out/server /out/worker /out/migrate /out/outbox /out/alertbridge /app/
COPY api/openapi.yml /app/api/openapi.yml
USER 10001:10001
EXPOSE 8080
CMD ["/app/server"]
