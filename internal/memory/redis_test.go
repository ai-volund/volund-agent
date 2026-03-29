package memory

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// mockRedisServer is a minimal RESP-speaking TCP server for testing.
type mockRedisServer struct {
	ln    net.Listener
	mu    sync.Mutex
	cmds  [][]string // recorded commands
	data  map[string]string
	list  map[string][]string
	conns []net.Conn
	wg    sync.WaitGroup
	done  chan struct{}
}

func newMockRedisServer(t *testing.T) *mockRedisServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock redis listen: %v", err)
	}
	s := &mockRedisServer{
		ln:   ln,
		data: make(map[string]string),
		list: make(map[string][]string),
		done: make(chan struct{}),
	}
	s.wg.Add(1)
	go s.serve(t)
	return s
}

func (s *mockRedisServer) addr() string {
	return s.ln.Addr().String()
}

func (s *mockRedisServer) close() {
	close(s.done)
	s.ln.Close()
	// Close all active client connections so handleConn goroutines unblock.
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *mockRedisServer) recordedCommands() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([][]string, len(s.cmds))
	copy(cp, s.cmds)
	return cp
}

func (s *mockRedisServer) serve(t *testing.T) {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				t.Logf("mock accept error: %v", err)
				return
			}
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handleConn(t, conn)
	}
}

func (s *mockRedisServer) handleConn(t *testing.T, conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for {
		args, err := s.readCommand(reader)
		if err != nil {
			return // client disconnected
		}
		if len(args) == 0 {
			continue
		}

		s.mu.Lock()
		s.cmds = append(s.cmds, args)
		s.mu.Unlock()

		cmd := strings.ToUpper(args[0])
		switch cmd {
		case "AUTH", "SELECT":
			conn.Write([]byte("+OK\r\n"))
		case "SET":
			if len(args) >= 3 {
				s.mu.Lock()
				s.data[args[1]] = args[2]
				s.mu.Unlock()
			}
			conn.Write([]byte("+OK\r\n"))
		case "GET":
			key := ""
			if len(args) >= 2 {
				key = args[1]
			}
			s.mu.Lock()
			val, ok := s.data[key]
			s.mu.Unlock()
			if !ok {
				conn.Write([]byte("$-1\r\n"))
			} else {
				conn.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(val), val)))
			}
		case "RPUSH":
			if len(args) >= 3 {
				s.mu.Lock()
				s.list[args[1]] = append(s.list[args[1]], args[2])
				length := len(s.list[args[1]])
				s.mu.Unlock()
				conn.Write([]byte(fmt.Sprintf(":%d\r\n", length)))
			} else {
				conn.Write([]byte(":0\r\n"))
			}
		case "LRANGE":
			key := ""
			if len(args) >= 2 {
				key = args[1]
			}
			s.mu.Lock()
			items := s.list[key]
			s.mu.Unlock()

			// Parse start/stop indices (support negative).
			start, stop := 0, -1
			if len(args) >= 3 {
				start, _ = strconv.Atoi(args[2])
			}
			if len(args) >= 4 {
				stop, _ = strconv.Atoi(args[3])
			}
			n := len(items)
			if start < 0 {
				start = n + start
			}
			if stop < 0 {
				stop = n + stop
			}
			if start < 0 {
				start = 0
			}
			if stop >= n {
				stop = n - 1
			}
			if start > stop || n == 0 {
				conn.Write([]byte("*0\r\n"))
			} else {
				subset := items[start : stop+1]
				var resp strings.Builder
				resp.WriteString(fmt.Sprintf("*%d\r\n", len(subset)))
				for _, v := range subset {
					resp.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(v), v))
				}
				conn.Write([]byte(resp.String()))
			}
		case "EXPIRE":
			conn.Write([]byte(":1\r\n"))
		default:
			conn.Write([]byte("+OK\r\n"))
		}
	}
}

// readCommand parses one RESP array command from the reader.
// Format: *N\r\n$len\r\narg\r\n...
func (s *mockRedisServer) readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")

	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected *, got %q", line)
	}
	count, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}

	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		// Read $N line.
		sizeLine, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeLine = strings.TrimRight(sizeLine, "\r\n")
		if !strings.HasPrefix(sizeLine, "$") {
			return nil, fmt.Errorf("expected $, got %q", sizeLine)
		}
		sz, err := strconv.Atoi(sizeLine[1:])
		if err != nil {
			return nil, err
		}
		// Read exactly sz bytes + \r\n.
		buf := make([]byte, sz+2)
		_, err = io.ReadFull(r, buf)
		if err != nil {
			return nil, err
		}
		args = append(args, string(buf[:sz]))
	}
	return args, nil
}

// newTestManager creates a RedisManager connected to the mock server.
func newTestManager(t *testing.T, srv *mockRedisServer, convID string) *RedisManager {
	t.Helper()
	rm, err := NewRedisManager(RedisConfig{Addr: srv.addr()}, convID, nil)
	if err != nil {
		t.Fatalf("NewRedisManager: %v", err)
	}
	t.Cleanup(func() { rm.Close() })
	return rm
}

func TestSessionKey(t *testing.T) {
	srv := newMockRedisServer(t)
	defer srv.close()

	rm := newTestManager(t, srv, "abc-123")

	got := rm.sessionKey("foo")
	want := "conv:abc-123:foo"
	if got != want {
		t.Errorf("sessionKey = %q, want %q", got, want)
	}
}

func TestSetConversation(t *testing.T) {
	srv := newMockRedisServer(t)
	defer srv.close()

	rm := newTestManager(t, srv, "old-id")

	// Before SetConversation, keys use old-id.
	if got := rm.sessionKey("x"); got != "conv:old-id:x" {
		t.Errorf("before SetConversation: sessionKey = %q", got)
	}

	rm.SetConversation("new-id")

	if got := rm.sessionKey("x"); got != "conv:new-id:x" {
		t.Errorf("after SetConversation: sessionKey = %q, want conv:new-id:x", got)
	}
}

func TestStoreSessionGetSession(t *testing.T) {
	srv := newMockRedisServer(t)
	defer srv.close()

	rm := newTestManager(t, srv, "sess-1")
	ctx := context.Background()

	err := rm.StoreSession(ctx, "theme", "dark")
	if err != nil {
		t.Fatalf("StoreSession: %v", err)
	}

	got, err := rm.GetSession(ctx, "theme")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got != "dark" {
		t.Errorf("GetSession = %q, want %q", got, "dark")
	}
}

func TestGetSessionMissing(t *testing.T) {
	srv := newMockRedisServer(t)
	defer srv.close()

	rm := newTestManager(t, srv, "sess-2")
	ctx := context.Background()

	got, err := rm.GetSession(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got != "" {
		t.Errorf("GetSession for missing key = %q, want empty string", got)
	}
}

func TestAppendMessage(t *testing.T) {
	srv := newMockRedisServer(t)
	defer srv.close()

	rm := newTestManager(t, srv, "conv-42")
	ctx := context.Background()

	err := rm.AppendMessage(ctx, "user", "hello agent")
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Verify the mock received RPUSH with the correct key.
	cmds := srv.recordedCommands()
	found := false
	for _, cmd := range cmds {
		if len(cmd) >= 3 && strings.ToUpper(cmd[0]) == "RPUSH" {
			if cmd[1] != "conv:conv-42:messages" {
				t.Errorf("RPUSH key = %q, want %q", cmd[1], "conv:conv-42:messages")
			}
			if cmd[2] != "user: hello agent" {
				t.Errorf("RPUSH value = %q, want %q", cmd[2], "user: hello agent")
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("no RPUSH command recorded")
	}
}

func TestGetHistory(t *testing.T) {
	srv := newMockRedisServer(t)
	defer srv.close()

	rm := newTestManager(t, srv, "hist-1")
	ctx := context.Background()

	// Append a few messages.
	if err := rm.AppendMessage(ctx, "user", "hi"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := rm.AppendMessage(ctx, "assistant", "hello there"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	got, err := rm.GetHistory(ctx, 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	// Should contain WrapExternal markers.
	if !strings.Contains(got, `[EXTERNAL_DATA label="session_history"]`) {
		t.Error("GetHistory missing EXTERNAL_DATA opening marker")
	}
	if !strings.Contains(got, "[/EXTERNAL_DATA]") {
		t.Error("GetHistory missing EXTERNAL_DATA closing marker")
	}

	// Should contain the sanitized messages.
	if !strings.Contains(got, "user: hi") {
		t.Error("GetHistory missing 'user: hi'")
	}
	if !strings.Contains(got, "assistant: hello there") {
		t.Error("GetHistory missing 'assistant: hello there'")
	}
}

func TestGetHistoryEmpty(t *testing.T) {
	srv := newMockRedisServer(t)
	defer srv.close()

	rm := newTestManager(t, srv, "empty-hist")
	ctx := context.Background()

	got, err := rm.GetHistory(ctx, 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if got != "" {
		t.Errorf("GetHistory on empty list = %q, want empty string", got)
	}
}
