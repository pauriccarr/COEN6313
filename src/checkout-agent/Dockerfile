FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN GOWORK=off go mod tidy
RUN GOWORK=off CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /checkoutservice .

FROM scratch

COPY --from=builder /checkoutservice /checkoutservice

EXPOSE 5050

ENTRYPOINT ["/checkoutservice"]
