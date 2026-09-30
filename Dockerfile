# syntax=docker/dockerfile:1

# 1. Build the React SPA (outputs into internal/assets/dist)
FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Build the Go binary, embedding the SPA. Pure-Go Postgres driver (pgx) => static binary.
FROM golang:1.24-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/internal/assets/dist ./internal/assets/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /libra ./cmd/libra

# 3. Minimal runtime
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /libra /libra
ENV LIBRA_ADDR=:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/libra"]
