FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# go.mod/go.sum are committed tidy; no `go mod tidy` here so builds are reproducible.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /checkoutservice .

FROM scratch

# CA roots, so TLS to external services (e.g. MongoDB Atlas) can verify.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /checkoutservice /checkoutservice

USER 1000:1000
# 5050 gRPC (hipstershop.CheckoutService), 8080 HTTP (/health, /ui)
EXPOSE 5050 8080

ENTRYPOINT ["/checkoutservice"]
