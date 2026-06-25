FROM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -o report-service .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata nodejs
COPY --from=builder /app/report-service /usr/local/bin/report-service
COPY eval-tool /app/eval-tool
ENV TZ=Asia/Tokyo
ENV EVAL_SCRIPT_PATH=/app/eval-tool/probe.mjs
EXPOSE 8090
CMD ["report-service"]
