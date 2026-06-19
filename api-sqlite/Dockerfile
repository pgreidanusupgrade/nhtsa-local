FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o vin-api .
# Provide an empty /tmp for the scratch image (no shell available there)
RUN mkdir /empty-tmp

FROM scratch
COPY --from=builder /empty-tmp /tmp
COPY --from=builder /app/vin-api /vin-api
EXPOSE 8080
ENTRYPOINT ["/vin-api"]
