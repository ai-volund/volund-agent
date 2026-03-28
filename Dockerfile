FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /volund-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /volund-agent /volund-agent

EXPOSE 8081

ENTRYPOINT ["/volund-agent"]
