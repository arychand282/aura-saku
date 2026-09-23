FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /aura-saku ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
ENV TZ=UTC
COPY --from=builder /aura-saku /aura-saku
EXPOSE 8080
ENTRYPOINT ["/aura-saku"]
