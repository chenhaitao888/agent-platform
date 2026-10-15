package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"agent-platform/backend/internal/review"
)

const (
	maxModelRequestBytes   = 8 << 20
	maxModelResponseBytes  = 32 << 20
	maxRelayFrameBytes     = 12 << 20
	modelChunkBytes        = 16 << 10
	maxModelRequests       = 64
	maxExecutionModelBytes = 256 << 20
)

// Private, newline-framed, full-duplex protocol over docker attach. No URL,
// authorization header, or upstream credential can be chosen by a frame.
type modelRelayFrame struct {
	Type    string             `json:"type"`
	Request *modelRelayRequest `json:"request,omitempty"`
	Head    *modelRelayHead    `json:"head,omitempty"`
	Data    []byte             `json:"data,omitempty"`
	Result  json.RawMessage    `json:"result,omitempty"`
}

type modelRelayRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   []byte `json:"body"`
}

type modelRelayHead struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
}

func relayScanner(input io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxRelayFrameBytes)
	return scanner
}

func readRelayFrame(scanner *bufio.Scanner) (modelRelayFrame, error) {
	var frame modelRelayFrame
	if !scanner.Scan() {
		return frame, review.ErrExecutionFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&frame) != nil || decoder.Decode(new(any)) != io.EOF {
		return modelRelayFrame{}, review.ErrInvalidFindings
	}
	return frame, nil
}

func modelHTTPClient(configured *http.Client, timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if configured != nil && configured.Transport != nil {
		selected, ok := configured.Transport.(*http.Transport)
		//nolint:staticcheck // A legacy TLS dialer also bypasses certificate policy; reject it rather than invoking it.
		if !ok || selected.DialTLSContext != nil || selected.DialTLS != nil {
			return nil, ErrInvalidRunnerConfig
		}
		transport = selected.Clone()
	}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		return nil, ErrInvalidRunnerConfig
	}
	transport.TLSClientConfig.ServerName = ""
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	// Preserve deployment TLS roots, never personal proxies, cookies, alternate
	// TLS dialers or redirect policy. Clone to freeze configuration and avoid
	// mutating or closing a caller-owned transport.
	transport.Proxy = nil
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.MaxIdleConnsPerHost = 2
	transport.IdleConnTimeout = time.Minute
	client := http.Client{Transport: transport}
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, nil
}

func validModelRequest(request *modelRelayRequest, model string) bool {
	if request == nil || request.Method != http.MethodPost || request.Path != "/responses" || len(request.Body) > maxModelRequestBytes {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	matched := false
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		if strings.EqualFold(name, "model") {
			var requested string
			if name != "model" || json.Unmarshal(value, &requested) != nil || requested != model {
				return false
			}
			matched = true
		}
	}
	closing, err := decoder.Token()
	return err == nil && closing == json.Delim('}') && decoder.Decode(new(any)) == io.EOF && matched
}

func (r *DockerRunner) relayModelRequest(ctx context.Context, request *modelRelayRequest, credential gatewayCredential, encoder *json.Encoder, remaining int64, usage *usageAccumulator) (int64, error) {
	if !validModelRequest(request, r.config.Model) || credential.validate(r.config.Gateway.ServicePrincipal, 0) != nil {
		return 0, review.ErrInvalidFindings
	}
	limit := min(int64(maxModelResponseBytes), remaining)
	if limit <= 0 {
		return 0, review.ErrOutputTooLarge
	}
	endpoint := strings.TrimRight(r.config.Gateway.BaseURL, "/") + "/responses"
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(request.Body))
	if err != nil {
		return 0, review.ErrExecutionFailed
	}
	upstream.Header.Set("Authorization", "Bearer "+credential.Token)
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", "text/event-stream, application/json")
	if usage != nil {
		usage.usage.Requests++
	}
	response, err := r.modelClient.Do(upstream)
	if err != nil {
		return 0, review.ErrExecutionFailed
	}
	defer func() { _ = response.Body.Close() }()
	// Error bodies/headers can contain arbitrary secrets, redirects, cookies or
	// origin information. They are not delivered to the Worker or persisted.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, review.ErrExecutionFailed
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (contentType != "text/event-stream" && contentType != "application/json") {
		return 0, review.ErrExecutionFailed
	}
	if encoder.Encode(modelRelayFrame{Type: "head", Head: &modelRelayHead{Status: response.StatusCode, ContentType: contentType}}) != nil {
		return 0, review.ErrExecutionFailed
	}
	reader := io.LimitReader(response.Body, limit+1)
	observer := usage.observe(contentType)
	cleanEOF := false
	defer func() { observer.finish(cleanEOF) }()
	buffer := make([]byte, modelChunkBytes)
	var total int64
	for {
		count, readErr := reader.Read(buffer)
		total += int64(count)
		if total > limit {
			return total, review.ErrOutputTooLarge
		}
		if count != 0 && encoder.Encode(modelRelayFrame{Type: "chunk", Data: buffer[:count]}) != nil {
			return total, review.ErrExecutionFailed
		}
		observer.write(buffer[:count])
		if readErr == io.EOF {
			cleanEOF = true
			break
		}
		if readErr != nil {
			return total, review.ErrExecutionFailed
		}
	}
	if encoder.Encode(modelRelayFrame{Type: "end"}) != nil {
		return total, review.ErrExecutionFailed
	}
	return total, nil
}

func (r *DockerRunner) relayCommand(ctx context.Context, input []byte, credential gatewayCredential, usage *usageAccumulator, args ...string) ([]byte, error) {
	home, err := os.MkdirTemp("", "agent-platform-docker-client-")
	if err != nil {
		return nil, review.ErrExecutionFailed
	}
	defer func() { _ = os.RemoveAll(home) }()
	command := exec.CommandContext(ctx, r.config.Binary, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "DOCKER_CONFIG=" + home, "DOCKER_HOST=" + r.config.Endpoint}
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, review.ErrExecutionFailed
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, review.ErrExecutionFailed
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, review.ErrExecutionFailed
	}
	// WaitDelay applies once Wait starts; a manual pipe read/write may still
	// block before it. Close our descriptors when canceled even if another
	// client process holds the peer end open.
	stopPipes := context.AfterFunc(ctx, func() { _ = stdin.Close(); _ = stdout.Close() })
	defer stopPipes()
	waited := false
	defer func() {
		_ = stdin.Close()
		_ = stdout.Close()
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	result, relayErr := r.exchangeModelFrames(ctx, stdin, stdout, input, credential, usage)
	if relayErr == nil {
		_ = stdin.Close()
		err = command.Wait()
		waited = true
		if err != nil {
			relayErr = review.ErrExecutionFailed
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result, relayErr
}

func (r *DockerRunner) exchangeModelFrames(ctx context.Context, input io.Writer, output io.Reader, initial []byte, credential gatewayCredential, usage *usageAccumulator) ([]byte, error) {
	encoder := json.NewEncoder(input)
	if encoder.Encode(json.RawMessage(initial)) != nil {
		return nil, review.ErrExecutionFailed
	}
	scanner := relayScanner(output)
	var transferred int64
	for calls := 0; ; {
		frame, err := readRelayFrame(scanner)
		if err != nil {
			return nil, err
		}
		switch frame.Type {
		case "request":
			if frame.Head != nil || len(frame.Data) != 0 || len(frame.Result) != 0 || calls >= maxModelRequests {
				return nil, review.ErrInvalidFindings
			}
			calls++
			if frame.Request != nil {
				transferred += int64(len(frame.Request.Body))
			}
			count, err := r.relayModelRequest(ctx, frame.Request, credential, encoder, maxExecutionModelBytes-transferred, usage)
			transferred += count
			if err != nil {
				return nil, err
			}
			if transferred > maxExecutionModelBytes {
				return nil, review.ErrOutputTooLarge
			}
		case "result":
			if frame.Request != nil || frame.Head != nil || len(frame.Data) != 0 || len(frame.Result) == 0 || len(frame.Result) > review.MaxFindingsBytes+8192 {
				return nil, review.ErrInvalidFindings
			}
			if scanner.Scan() || scanner.Err() != nil {
				return nil, review.ErrInvalidFindings
			}
			return frame.Result, nil
		default:
			return nil, review.ErrInvalidFindings
		}
	}
}
