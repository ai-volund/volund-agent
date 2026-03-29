package memory

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ai-volund/volund-agent/internal/safety"
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

// SetConversation updates the conversation ID used to scope session keys.
func (r *RedisManager) SetConversation(convID string) {
	r.convID = convID
}

// AppendMessage appends a chat message to the conversation's session history.
// Messages are stored as a Redis LIST with key conv:{convID}:messages.
func (r *RedisManager) AppendMessage(ctx context.Context, role, content string) error {
	key := r.sessionKey("messages")
	entry := role + ": " + content
	if err := r.do("RPUSH", key, entry); err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	// Ensure the list has a TTL so it doesn't persist indefinitely.
	_ = r.do("EXPIRE", key, strconv.Itoa(int(SessionTTL.Seconds())))
	return nil
}

// GetHistory returns the last N messages from session history as a formatted
// string suitable for prompt injection.
func (r *RedisManager) GetHistory(ctx context.Context, limit int) (string, error) {
	key := r.sessionKey("messages")
	// LRANGE with negative indices: -limit to -1 gets the last `limit` entries.
	start := fmt.Sprintf("-%d", limit)
	entries, err := r.doList("LRANGE", key, start, "-1")
	if err != nil {
		return "", fmt.Errorf("get history: %w", err)
	}
	if len(entries) == 0 {
		return "", nil
	}
	var inner strings.Builder
	for _, e := range entries {
		inner.WriteString(safety.SanitizeMemory(e))
		inner.WriteByte('\n')
	}
	return "\n\n" + safety.WrapExternal("session_history", inner.String()), nil
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

// doList executes a command that returns a Redis array (e.g. LRANGE).
func (r *RedisManager) doList(args ...string) ([]string, error) {
	if err := r.writeCommand(args); err != nil {
		return nil, err
	}
	return r.readArray()
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

// readArray parses a RESP array response (e.g. from LRANGE).
// Format: *N\r\n$len\r\nvalue\r\n$len\r\nvalue\r\n...
func (r *RedisManager) readArray() ([]string, error) {
	var buf [65536]byte
	n, err := r.conn.Read(buf[:])
	if err != nil && err != io.EOF {
		return nil, err
	}
	resp := string(buf[:n])

	// Empty array or nil: *0\r\n or *-1\r\n
	if strings.HasPrefix(resp, "*0") || strings.HasPrefix(resp, "*-1") {
		return nil, nil
	}
	// Error response
	if strings.HasPrefix(resp, "-") {
		return nil, fmt.Errorf("redis: %s", strings.TrimRight(resp[1:], "\r\n"))
	}

	lines := strings.Split(resp, "\r\n")
	if len(lines) < 1 || !strings.HasPrefix(lines[0], "*") {
		return nil, fmt.Errorf("redis: unexpected array response: %q", resp)
	}

	count, parseErr := strconv.Atoi(lines[0][1:])
	if parseErr != nil {
		return nil, fmt.Errorf("redis: bad array count: %w", parseErr)
	}

	result := make([]string, 0, count)
	i := 1
	for len(result) < count && i < len(lines) {
		if strings.HasPrefix(lines[i], "$") {
			// Next line is the value.
			i++
			if i < len(lines) {
				result = append(result, lines[i])
			}
		}
		i++
	}
	return result, nil
}
