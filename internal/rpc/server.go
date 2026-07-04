package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
)

// Handler serves one method. Params arrive raw; return value is marshalled.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server accepts unix-socket connections, dispatches requests to handlers,
// and fans events out to subscribed connections.
type Server struct {
	socketPath string
	handlers   map[string]Handler

	mu   sync.Mutex
	subs map[*conn]struct{}
	ln   net.Listener
}

type conn struct {
	c  net.Conn
	mu sync.Mutex // serializes writes (responses vs pushed events)
}

func (c *conn) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.c.Write(append(b, '\n'))
	return err
}

func NewServer(socketPath string) *Server {
	return &Server{
		socketPath: socketPath,
		handlers:   map[string]Handler{},
		subs:       map[*conn]struct{}{},
	}
}

func (s *Server) Handle(method string, h Handler) { s.handlers[method] = h }

// Listen binds the socket, replacing a stale one if no daemon answers on it.
func (s *Server) Listen() error {
	if _, err := os.Stat(s.socketPath); err == nil {
		// Socket file exists: live daemon or stale leftover?
		if c, err := net.Dial("unix", s.socketPath); err == nil {
			c.Close()
			return fmt.Errorf("daemon already running on %s", s.socketPath)
		}
		if err := os.Remove(s.socketPath); err != nil {
			return err
		}
	}
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.ln.Close()
	}()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, &conn{c: c})
	}
}

func (s *Server) serveConn(ctx context.Context, c *conn) {
	defer func() {
		s.mu.Lock()
		delete(s.subs, c)
		s.mu.Unlock()
		c.c.Close()
	}()
	sc := bufio.NewScanner(c.c)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var req Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			c.writeJSON(Response{Error: "bad request: " + err.Error()})
			continue
		}
		if req.Method == "events.subscribe" {
			s.mu.Lock()
			s.subs[c] = struct{}{}
			s.mu.Unlock()
			c.writeJSON(Response{ID: req.ID, Result: json.RawMessage(`"subscribed"`)})
			continue
		}
		h, ok := s.handlers[req.Method]
		if !ok {
			c.writeJSON(Response{ID: req.ID, Error: "unknown method: " + req.Method})
			continue
		}
		// Handlers run inline: daemon methods are fast db ops; long agent work
		// happens on daemon goroutines and reports back through events.
		res, err := h(ctx, req.Params)
		if err != nil {
			c.writeJSON(Response{ID: req.ID, Error: err.Error()})
			continue
		}
		b, err := json.Marshal(res)
		if err != nil {
			c.writeJSON(Response{ID: req.ID, Error: err.Error()})
			continue
		}
		c.writeJSON(Response{ID: req.ID, Result: b})
	}
}

// Publish sends an event to all subscribed connections. Send failures drop
// the subscriber; its connection is already dead or dying.
func (s *Server) Publish(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	ev := Event{Event: event, Data: b}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.subs {
		if err := c.writeJSON(ev); err != nil {
			delete(s.subs, c)
		}
	}
}

var ErrNoDaemon = errors.New("daemon not running")
