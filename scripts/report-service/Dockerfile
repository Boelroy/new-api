FROM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -o report-service .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata nodejs npm git
# Claude Code headless grader. ANTHROPIC_API_KEY (via CLAUDE_GRADER_API_KEY)
# is injected at runtime when invoking `claude -p`.
RUN npm install -g @anthropic-ai/claude-code && claude --version
COPY --from=builder /app/report-service /usr/local/bin/report-service
COPY eval-tool /app/eval-tool
COPY detect-tool /app/detect-tool
ENV TZ=Asia/Tokyo
ENV EVAL_SCRIPT_PATH=/app/eval-tool/probe.mjs
ENV EVAL_PIPELINE_PATH=/app/eval-tool/PIPELINE.md
ENV DETECT_PIPELINE_PATH=/app/detect-tool/PIPELINE.md
EXPOSE 8090
CMD ["report-service"]
