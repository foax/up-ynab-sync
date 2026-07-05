FROM golang:1.26.3 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/up-ynab-sync .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/up-ynab-sync /up-ynab-sync

ENTRYPOINT ["/up-ynab-sync"]
