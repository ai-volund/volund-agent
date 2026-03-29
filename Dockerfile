FROM golang:1.26-bookworm AS builder

# Build from workspace root so the ../volund-proto replace directive resolves.
WORKDIR /workspace

# Copy local proto dependency first — own layer for caching.
COPY volund-proto/ volund-proto/

# Copy module manifests so `go mod download` is cached separately from source.
COPY volund-agent/go.mod volund-agent/go.sum volund-agent/
WORKDIR /workspace/volund-agent
RUN go mod download

# Copy full module source and build.
COPY volund-agent/ .
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags "-s -w" -o /bin/volund-agent ./cmd/agent
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags "-s -w" -o /bin/mcp-echo ./cmd/mcp-echo
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags "-s -w" -o /bin/mcp-cli-adapter ./cmd/mcp-cli-adapter

# Build skills that ship with the base image.
WORKDIR /workspace
COPY volund-skills/skills/email/ volund-skills/skills/email/
WORKDIR /workspace/volund-skills/skills/email
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags "-s -w" -o /bin/mcp-email .

# Use a slim Debian image (not distroless) so the run_code tool has access
# to python3 and bash for code execution.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    nodejs \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/volund-agent /bin/volund-agent
COPY --from=builder /bin/mcp-echo /usr/local/bin/mcp-echo
COPY --from=builder /bin/mcp-cli-adapter /usr/local/bin/mcp-cli-adapter
COPY --from=builder /bin/mcp-email /usr/local/bin/mcp-email

ENTRYPOINT ["/bin/volund-agent"]
