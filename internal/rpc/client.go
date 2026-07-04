package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// Client is a unix-socket connection to the daemon. Safe for concurrent use.
// Events arrive on Events() after Subscribe.
type Client struct {
	c      net.Conn
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan Response

	events chan Event
	done   chan struct{}
	err    error
}

func Dial(socketPath string) (*Client, error) {
	c, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, ErrNoDaemon
	}
	cl := &Client{
		c:       c,
		pending: map[int64]chan Response{},
		events:  make(chan Event, 256),
		done:    make(chan struct{}),
	}
	go cl.readLoop()
	return cl, nil
}

func (cl *Client) readLoop() {
	sc := bufio.NewScanner(cl.c)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		var probe struct {
			ID    *int64 `json:"id"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		if probe.Event != "" {
			var ev Event
			if json.Unmarshal(line, &ev) == nil {
				select {
				case cl.events <- ev:
				default: // slow consumer: drop rather than deadlock the socket
				}
			}
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		cl.mu.Lock()
		ch, ok := cl.pending[resp.ID]
		delete(cl.pending, resp.ID)
		cl.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
	cl.err = sc.Err()
	close(cl.done)
	close(cl.events)
}

// Call issues a request and decodes the result into out (out may be nil).
func (cl *Client) Call(method string, params any, out any) error {
	id := cl.nextID.Add(1)
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	b, err := json.Marshal(Request{ID: id, Method: method, Params: raw})
	if err != nil {
		return err
	}
	ch := make(chan Response, 1)
	cl.mu.Lock()
	cl.pending[id] = ch
	cl.mu.Unlock()
	if _, err := cl.c.Write(append(b, '\n')); err != nil {
		cl.mu.Lock()
		delete(cl.pending, id)
		cl.mu.Unlock()
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != "" {
			return fmt.Errorf("%s", resp.Error)
		}
		if out != nil && resp.Result != nil {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	case <-cl.done:
		return fmt.Errorf("connection closed")
	}
}

func (cl *Client) Subscribe() error {
	return cl.Call("events.subscribe", nil, nil)
}

func (cl *Client) Events() <-chan Event { return cl.events }

func (cl *Client) Close() error { return cl.c.Close() }
