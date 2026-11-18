package mcpbridge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"agent-platform/backend/internal/planning"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Connection struct {
	Transport           string   `json:"transport"`
	URL                 string   `json:"url,omitempty"`
	Command             string   `json:"command,omitempty"`
	Args                []string `json:"args,omitempty"`
	Directory           string   `json:"directory,omitempty"`
	CAFile              string   `json:"caFile,omitempty"`
	ReadCredentialFile  string   `json:"readCredentialFile,omitempty"`
	WriteCredentialFile string   `json:"writeCredentialFile,omitempty"`
}
type Credentials struct {
	Headers map[string]string `json:"headers,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}
type Tool struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	InputSchema  any    `json:"inputSchema"`
	OutputSchema any    `json:"outputSchema,omitempty"`
	Annotations  any    `json:"annotations,omitempty"`
}

func (t Tool) Hash() string { return planning.Hash(t) }

type Catalog struct {
	Tools  []Tool `json:"tools"`
	Digest string `json:"digest"`
}

// Client holds no sessions between calls. Each attempt discovers the current
// schema before dispatch. Writes are NEVER retried by this adapter/HTTP client.
type Client struct {
	Connection  Connection
	Credentials Credentials
	TLS         *tls.Config
}

func (c Client) Validate() error {
	switch c.Connection.Transport {
	case "streamable-http", "sse":
		u, err := url.Parse(c.Connection.URL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.Connection.Command != "" || len(c.Connection.Args) != 0 || c.Connection.Directory != "" || len(c.Credentials.Env) != 0 {
			return ErrBinding
		}
		localHTTP := u.Scheme == "http" && net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback()
		if u.Scheme != "https" && !localHTTP {
			return ErrBinding
		}
		for key, value := range c.Credentials.Headers {
			if key == "" || strings.ContainsAny(key+value, "\r\n\x00") || slices.Contains([]string{"host", "content-type", "accept", "content-length", "connection", "mcp-session-id", "mcp-protocol-version", "idempotency-key", "x-idempotency-key"}, strings.ToLower(key)) {
				return ErrBinding
			}
		}
	case "stdio":
		if !filepath.IsAbs(c.Connection.Command) || c.Connection.URL != "" || c.Connection.CAFile != "" || !filepath.IsAbs(c.Connection.Directory) || len(c.Credentials.Headers) != 0 || len(c.Connection.Args) > 64 {
			return ErrBinding
		}
		for key, value := range c.Credentials.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
				return ErrBinding
			}
		}
	default:
		return ErrBinding
	}
	return nil
}
func (c Client) connect(ctx context.Context) (*mcp.ClientSession, error) {
	if c.Validate() != nil {
		return nil, ErrBinding
	}
	var transport mcp.Transport
	if c.Connection.Transport == "stdio" {
		cmd := exec.CommandContext(ctx, c.Connection.Command, c.Connection.Args...)
		cmd.Dir = c.Connection.Directory
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
		for key, value := range c.Credentials.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		cmd.Stderr = io.Discard
		cmd.WaitDelay = time.Second
		transport = &mcp.CommandTransport{Command: cmd}
	} else {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = nil
		base.TLSClientConfig = c.TLS
		base.ResponseHeaderTimeout = 10 * time.Second
		// Every operation has a short-lived session/transport. Release pooled
		// sockets when the operation ends instead of accumulating idle pools.
		context.AfterFunc(ctx, base.CloseIdleConnections)
		client := &http.Client{Transport: &scopedTransport{base: base, endpoint: c.Connection.URL, headers: c.Credentials.Headers}, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrConnection }}
		if c.Connection.Transport == "sse" {
			transport = &mcp.SSEClientTransport{Endpoint: c.Connection.URL, HTTPClient: client}
		} else {
			transport = &mcp.StreamableClientTransport{Endpoint: c.Connection.URL, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: 2 << 20}
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "agent-platform", Version: "company.v1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, ErrConnection
	}
	return session, nil
}

type scopedTransport struct {
	base     *http.Transport
	endpoint string
	headers  map[string]string
}

func (t *scopedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(t.endpoint)
	if r.URL.Scheme != u.Scheme || r.URL.Host != u.Host || r.URL.User != nil {
		return nil, ErrConnection
	}
	r = r.Clone(r.Context())
	r.GetBody = nil
	// net/http treats Idempotency-Key as permission to transparently retry a
	// POST on a broken reused socket. Idempotency is carried inside tool args.
	r.Header.Del("Idempotency-Key")
	r.Header.Del("X-Idempotency-Key")
	for key, value := range t.headers {
		r.Header.Set(key, value)
	}
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, ErrConnection
	}
	response.Body = &limitedBody{Reader: io.LimitReader(response.Body, 2<<20), Closer: response.Body}
	return response, nil
}

type limitedBody struct {
	io.Reader
	io.Closer
}

func discover(ctx context.Context, session *mcp.ClientSession) (Catalog, error) {
	result := Catalog{Tools: []Tool{}}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 32; page++ {
		list, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return Catalog{}, ErrConnection
		}
		for _, item := range list.Tools {
			if item.Name == "" || seen["tool:"+item.Name] || len(result.Tools) >= 256 {
				return Catalog{}, ErrBinding
			}
			seen["tool:"+item.Name] = true
			result.Tools = append(result.Tools, Tool{Name: item.Name, Description: item.Description, InputSchema: item.InputSchema, OutputSchema: item.OutputSchema, Annotations: item.Annotations})
		}
		cursor = list.NextCursor
		if cursor == "" {
			slices.SortFunc(result.Tools, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
			result.Digest = planning.Hash(result.Tools)
			data, _ := json.Marshal(result)
			if len(data) > 1<<20 {
				return Catalog{}, ErrBinding
			}
			return result, nil
		}
		if seen["cursor:"+cursor] {
			return Catalog{}, ErrBinding
		}
		seen["cursor:"+cursor] = true
	}
	return Catalog{}, ErrBinding
}
func (c Client) Discover(ctx context.Context) (Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s, err := c.connect(ctx)
	if err != nil {
		return Catalog{}, err
	}
	defer func() { _ = s.Close() }()
	return discover(ctx, s)
}

func validateSchema(schema, value any) error {
	data, err := json.Marshal(schema)
	if err != nil {
		return ErrBinding
	}
	var s jsonschema.Schema
	if json.Unmarshal(data, &s) != nil {
		return ErrBinding
	}
	resolved, err := s.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil || resolved.Validate(value) != nil {
		return ErrBinding
	}
	return nil
}

func (c Client) call(ctx context.Context, binding Call, scope map[string]any, destination any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()
	catalog, err := discover(ctx, session)
	if err != nil {
		return err
	}
	var tool *Tool
	for n := range catalog.Tools {
		if catalog.Tools[n].Name == binding.Tool {
			tool = &catalog.Tools[n]
			break
		}
	}
	if tool == nil || tool.Hash() != binding.ToolHash {
		return ErrBinding
	}
	arguments, err := Render(binding.Arguments, scope)
	if err != nil || validateSchema(tool.InputSchema, arguments) != nil {
		return ErrBinding
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: binding.Tool, Arguments: arguments})
	if err != nil || result.IsError || result.NeedsInput() || len(result.InputRequests) > 0 {
		return ErrConnection
	}
	var raw any
	if binding.ResultEncoding == "json-text" {
		if len(result.Content) != 1 {
			return ErrBinding
		}
		content, ok := result.Content[0].(*mcp.TextContent)
		if !ok || len(content.Text) > 1<<20 || decodeJSON([]byte(content.Text), &raw) != nil {
			return ErrBinding
		}
	} else {
		raw = result.StructuredContent
	}
	if raw == nil {
		return ErrBinding
	}
	data, err := json.Marshal(raw)
	if err != nil || len(data) > 1<<20 || decodeJSON(data, &raw) != nil {
		return ErrBinding
	}
	if tool.OutputSchema != nil && validateSchema(tool.OutputSchema, raw) != nil {
		return ErrBinding
	}
	scope["result"] = raw
	mapped, err := Render(binding.Result, scope)
	if err != nil {
		return err
	}
	data, err = json.Marshal(mapped)
	if err != nil {
		return ErrBinding
	}
	if err := strictDecode(data, destination); err != nil {
		return err
	}
	return nil
}
