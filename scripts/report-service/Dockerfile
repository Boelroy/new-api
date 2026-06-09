FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o report-service .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/report-service /usr/local/bin/report-service
ENV TZ=Asia/Shanghai
EXPOSE 8090
CMD ["report-service"]
