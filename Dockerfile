FROM golang:1.26-alpine AS builder
WORKDIR /app

COPY . .

# Init module if not present (safe)
RUN apk add --no-cache ca-certificates git
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -o app


# ---------- Final ----------
FROM scratch
COPY --from=builder /app/app /app
# copy CA certs
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["/app"]