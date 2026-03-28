package llm

import (
	"context"
	"fmt"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is a gRPC client for the Volund LLM Router service.
type Client struct {
	conn   *grpc.ClientConn
	client volundv1.LLMServiceClient
}

// NewClient creates a new LLM Router gRPC client.
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to LLM Router at %s: %w", addr, err)
	}

	return &Client{
		conn:   conn,
		client: volundv1.NewLLMServiceClient(conn),
	}, nil
}

// Chat sends a non-streaming chat request to the LLM Router.
func (c *Client) Chat(ctx context.Context, req *volundv1.ChatRequest) (*volundv1.ChatResponse, error) {
	resp, err := c.client.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("LLM chat request failed: %w", err)
	}
	return resp, nil
}

// StreamChat opens a streaming chat request to the LLM Router.
// The returned stream receives TextDelta, ToolUse, and Complete chunks.
func (c *Client) StreamChat(ctx context.Context, req *volundv1.StreamChatRequest) (volundv1.LLMService_StreamChatClient, error) {
	stream, err := c.client.StreamChat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("LLM stream chat request failed: %w", err)
	}
	return stream, nil
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
