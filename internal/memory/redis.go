package memory

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// SessionTTL is how long session keys persist in Redis.
const SessionTTL = 24 * time.Hour

// RedisManager implements the Manager interface with Redis for session storage
// and delegates to a LongTermStore for vector-based long-term memory.
type RedisManager struct {
	conn     net.Conn
	convID   string
	longTerm LongTermStore
}

// LongTermStore is the interface for long-term memory operations.
// Implemented by the gateway memory API client.
type LongTermStore interface {
	Store(ctx context.Context, mem Memory) error
	Search(ctx context.Context, query string, limit int) ([]Memory, error)
}

// RedisConfig holds Redis connection parameters.
type RedisConfig struct {
	Addr     string // host:port
	Password string
	DB       int
}

// NewRedisManager creates a Manager backed by Redis for session storage.
// convID scopes all session keys to the conversation.
// longTerm may be nil (falls back to noop for long-term operations).
func NewRedisManager(cfg RedisConfig, convID string, longTerm LongTermStore) (*RedisManager, error) {
	if cfg.Addr == "" {
		cfg.Addr = "localhost:6379"
	}

	conn, err := net.DialTimeout("tcp", cfg.Addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("redis connect: %w", err)
	}

	rm := &RedisManager{conn: conn, convID: convID, longTerm: longTerm}

	// AUTH if password set.
	if cfg.Password != "" {
		if err := rm.do("AUTH", cfg.Password); err != nil {
			conn.Close()
			return nil, fmt.Errorf("redis auth: %w", err)
		}
	}
	// SELECT db if non-zero.
	if cfg.DB != 0 {
		if err := rm.do("SELECT", strconv.Itoa(cfg.DB)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("redis select: %w", err)
		}
	}

	return rm, nil
}

func (r *RedisManager) sessionKey(key string) string {
	return fmt.Sprintf("conv:%s:%s", r.convID, key)
}

// StoreSession stores a key-value pair in Redis with a conversation-scoped key.
func (r *RedisManager) StoreSession(ctx context.Context, key, value string) error {
	sk := r.sessionKey(key)
	return r.do("SET", sk, value, "EX", strconv.Itoa(int(SessionTTL.Seconds())))
}

// GetSession retrieves a session value from Redis.
func (r *RedisManager) GetSession(ctx context.Context, key string) (string, error) {
	sk := r.sessionKey(key)
	return r.doString("GET", sk)
}

// StoreLongTerm delegates to the LongTermStore if available.
func (r *RedisManager) StoreLongTerm(ctx context.Context, mem Memory) error {
	if r.longTerm == nil {
		return nil
	}
	return r.longTerm.Store(ctx, mem)
}

// SearchSimilar delegates to the LongTermStore if available.
func (r *RedisManager) SearchSimilar(ctx context.Context, query string, limit int) ([]Memory, error) {
	if r.longTerm == nil {
		return nil, nil
	}
	return r.longTerm.Search(ctx, query, limit)
}

// RetrieveContext searches for relevant memories and formats them for prompt injection.
func (r *RedisManager) RetrieveContext(ctx context.Context, query string, limit int) string {
	memories, err := r.SearchSimilar(ctx, query, limit)
	if err != nil || len(memories) == 0 {
		return ""
	}
	return FormatMemories(memories)
}

// Close closes the Redis connection.
func (r *RedisManager) Close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// --- Minimal RESP protocol implementation ---
// We use a raw TCP connection to avoid pulling in a large Redis client dependency.

func (r *RedisManager) do(args ...string) error {
	if err := r.writeCommand(args); err != nil {
		return err
	}
	_, err := r.readLine()
	return err
}

func (r *RedisManager) doString(args ...string) (string, error) {
	if err := r.writeCommand(args); err != nil {
		return "", err
	}
	return r.readBulkString()
}

func (r *RedisManager) writeCommand(args []string) error {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*%d\r\n", len(args)))
	for _, arg := range args {
		sb.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(arg), arg))
	}
	_, err := r.conn.Write([]byte(sb.String()))
	return err
}

func (r *RedisManager) readLine() (string, error) {
	var buf [4096]byte
	n, err := r.conn.Read(buf[:])
	if err != nil {
		return "", err
	}
	line := strings.TrimRight(string(buf[:n]), "\r\n")
	if len(line) > 0 && line[0] == '-' {
		return "", fmt.Errorf("redis: %s", line[1:])
	}
	return line, nil
}

func (r *RedisManager) readBulkString() (string, error) {
	var buf [8192]byte
	n, err := r.conn.Read(buf[:])
	if err != nil && err != io.EOF {
		return "", err
	}
	resp := string(buf[:n])

	// $-1\r\n = nil (key not found)
	if strings.HasPrefix(resp, "$-1") {
		return "", nil
	}
	// $N\r\n<data>\r\n
	if strings.HasPrefix(resp, "$") {
		parts := strings.SplitN(resp, "\r\n", 3)
		if len(parts) >= 2 {
			return parts[1], nil
		}
	}
	// Simple string: +OK\r\n
	if strings.HasPrefix(resp, "+") {
		return strings.TrimPrefix(resp, "+"), nil
	}
	// Error
	if strings.HasPrefix(resp, "-") {
		return "", fmt.Errorf("redis: %s", resp[1:])
	}
	return resp, nil
}
