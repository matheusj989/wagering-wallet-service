FROM golang:1.27.1-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 wallet
COPY --from=build /out/api /usr/local/bin/api

USER wallet
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
