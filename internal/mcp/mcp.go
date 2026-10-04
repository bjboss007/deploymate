// Package mcp is DeployMate's Model Context Protocol server. It speaks JSON-RPC
// over stdio and turns each tool call into one request to the dashboard's
// /api/v1 with an API token — it never touches the database or Docker itself,
// so it can run on a laptop against a remote DeployMate, and everything it can
// do is exactly what the token allows (docs/decisions/0020-api-tokens.md).
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// protocolVersion is the MCP revision this server implements; a client that
// asks for another is told this one and decides whether to continue.
const protocolVersion = "2025-06-18"

// maxResultBytes caps what one tool call returns to the model.
const maxResultBytes = 30000

// Server serves MCP over a reader/writer pair.
type Server struct {
	baseURL string
	token   string
	http    *http.Client
	version string
	log     io.Writer // stderr: stdout belongs to the protocol

	mu    sync.Mutex // serialises writes to out
	out   io.Writer
	scope string // the token's scope, learned lazily ("" until known)
}

// New builds a server for the dashboard at baseURL using token.
func New(baseURL, token, version string, log io.Writer) *Server {
	return &Server{
		baseURL: strings.TrimRight(baseURL, "/"), token: token, version: version, log: log,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads newline-delimited JSON-RPC messages from in until it closes.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.write(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		s.handle(ctx, req)
	}
	return sc.Err()
}

func (s *Server) write(r response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(r)
	_, _ = s.out.Write(append(b, '\n'))
}

func (s *Server) reply(req request, result any) {
	s.write(response{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *Server) fail(req request, code int, msg string) {
	s.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{code, msg}})
}

func (s *Server) handle(ctx context.Context, req request) {
	isNotification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		s.reply(req, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "deploymate", "version": s.version},
			"instructions": "Operate a DeployMate server. Start with fleet_status. Tools you can see are the ones this token's scope allows. " +
				"Secrets are never readable: variables show names only. Deleting anything is not available here.",
		})
	case "notifications/initialized", "notifications/cancelled":
		// nothing to do
	case "ping":
		s.reply(req, map[string]any{})
	case "tools/list":
		s.reply(req, map[string]any{"tools": s.visibleTools(ctx)})
	case "tools/call":
		s.reply(req, s.callTool(ctx, req.Params))
	default:
		if !isNotification {
			s.fail(req, -32601, "method not found: "+req.Method)
		}
	}
}

// ---- API access -------------------------------------------------------------

// api performs one request against the dashboard. It returns the status, the
// raw body, and an error only for transport failures.
func (s *Server) api(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+"/api/v1"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, err
}

// learnScope asks the dashboard what the token may do (once).
func (s *Server) learnScope(ctx context.Context) string {
	s.mu.Lock()
	known := s.scope
	s.mu.Unlock()
	if known != "" {
		return known
	}
	status, b, err := s.api(ctx, http.MethodGet, "/whoami", nil)
	scope := store.ScopeRead // unknown: show the least, never the most
	if err == nil && status == http.StatusOK {
		var who struct {
			Scope string `json:"scope"`
		}
		if json.Unmarshal(b, &who) == nil && store.ScopeRank(who.Scope) > 0 {
			scope = who.Scope
		}
	} else if s.log != nil {
		fmt.Fprintf(s.log, "deploymate mcp: could not check the token (%v, status %d)\n", err, status)
		return scope // do not cache a failure
	}
	s.mu.Lock()
	s.scope = scope
	s.mu.Unlock()
	return scope
}

var errNotAllowed = errors.New("this token's scope does not allow that tool")
