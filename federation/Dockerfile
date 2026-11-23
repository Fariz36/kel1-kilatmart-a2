# --- build: compile the Go service (cgo, because DuckDB is a C++ library) ---
FROM golang:1.26.11-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/federation .
# Download the DuckDB postgres + mysql extensions now, so the demo runs offline.
RUN /out/federation -install-extensions

# --- runtime ---
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 app
COPY --from=build /out/federation /app/federation
COPY --from=build /opt/duckdb /opt/duckdb
USER app
EXPOSE 8080
ENTRYPOINT ["/app/federation"]
