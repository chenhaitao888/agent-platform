package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"agent-platform/backend/internal/review"
)

// ServeModelRelayWorker owns input and closes it on cancellation/completion.
// Only the CLI parent can use its authenticated loopback endpoint; normal
// native read-only tool commands run in a separate network namespace.
func ServeModelRelayWorker(ctx context.Context, config ExecConfig, probe bool, input io.ReadCloser, output io.Writer) error {
	if probe {
		return ServeWorker(ctx, config, true, input, output)
	}
	execution, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()
	stopInput := context.AfterFunc(execution, func() { _ = input.Close() })
	defer func() { stopInput(); _ = input.Close() }()
	scanner := relayScanner(input)
	encoder := json.NewEncoder(output)
	finish := func(code string) error {
		content, _ := json.Marshal(workerResponse{ErrorCode: code})
		return encoder.Encode(modelRelayFrame{Type: "result", Result: content})
	}
	if !scanner.Scan() || len(scanner.Bytes()) > 6*review.MaxReviewPatchBytes+16384 {
		return finish("review_input_invalid")
	}
	initial := append([]byte(nil), scanner.Bytes()...)
	var envelope workerGatewayRequest
	decoder := json.NewDecoder(bytes.NewReader(initial))
	decoder.DisallowUnknownFields()
	gateway, err := validatedGateway(config.Gateway)
	if err != nil || gateway == nil || decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Input.Validate() != nil {
		return finish("review_input_invalid")
	}
	credential, err := parseGatewayCredential(envelope.Credential, gateway.ServicePrincipal, config.Timeout)
	if err != nil {
		return finish("review_credentials_unavailable")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return finish("review_execution_failed")
	}
	relay := &workerModelRelay{ctx: execution, scanner: scanner, encoder: encoder, model: config.Model, token: credential.Token}
	server := &http.Server{Handler: relay, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: config.Timeout,
		MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	served := make(chan struct{})
	go func() { _ = server.Serve(listener); close(served) }()
	stopServer := context.AfterFunc(execution, func() { _ = server.Close() })
	config.modelBaseURL = "http://" + listener.Addr().String()
	var result bytes.Buffer
	runErr := ServeWorker(execution, config, false, bytes.NewReader(initial), &result)
	cancel()
	stopServer()
	_ = server.Close()
	_ = input.Close()
	<-served
	// Ensure an in-flight response cannot interleave with the final result.
	relay.mutex.Lock()
	defer relay.mutex.Unlock()
	if runErr != nil {
		return finish("review_execution_failed")
	}
	return encoder.Encode(modelRelayFrame{Type: "result", Result: bytes.TrimSpace(result.Bytes())})
}

type workerModelRelay struct {
	ctx          context.Context
	scanner      *bufio.Scanner
	encoder      *json.Encoder
	model, token string
	mutex        sync.Mutex
}

func (relay *workerModelRelay) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte("Bearer "+relay.token)) != 1 {
		http.Error(w, "model authentication required", http.StatusUnauthorized)
		return
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, maxModelRequestBytes+1))
	modelRequest := &modelRelayRequest{Method: request.Method, Path: request.URL.RequestURI(), Body: content}
	if err != nil || !validModelRequest(modelRequest, relay.model) {
		http.Error(w, "model request rejected", http.StatusBadRequest)
		return
	}
	relay.mutex.Lock()
	defer relay.mutex.Unlock()
	if relay.ctx.Err() != nil {
		return
	}
	if err := relay.exchange(w, modelRequest); err != nil {
		// Abort a partially streamed response; never turn it into a valid SSE.
		panic(http.ErrAbortHandler)
	}
}

func (relay *workerModelRelay) exchange(w http.ResponseWriter, request *modelRelayRequest) error {
	if relay.encoder.Encode(modelRelayFrame{Type: "request", Request: request}) != nil {
		return review.ErrExecutionFailed
	}
	frame, err := readRelayFrame(relay.scanner)
	if err != nil || frame.Type != "head" || frame.Head == nil || frame.Request != nil || len(frame.Data) != 0 || len(frame.Result) != 0 ||
		frame.Head.Status < 200 || frame.Head.Status >= 300 || (frame.Head.ContentType != "text/event-stream" && frame.Head.ContentType != "application/json") {
		return review.ErrExecutionFailed
	}
	w.Header().Set("Content-Type", frame.Head.ContentType)
	w.WriteHeader(frame.Head.Status)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
	var total int
	for {
		frame, err := readRelayFrame(relay.scanner)
		if err != nil || frame.Request != nil || frame.Head != nil || len(frame.Result) != 0 {
			return review.ErrExecutionFailed
		}
		if frame.Type == "end" && len(frame.Data) == 0 {
			return nil
		}
		if frame.Type != "chunk" || len(frame.Data) == 0 || len(frame.Data) > modelChunkBytes {
			return review.ErrExecutionFailed
		}
		total += len(frame.Data)
		if total > maxModelResponseBytes {
			return review.ErrOutputTooLarge
		}
		if _, err := w.Write(frame.Data); err != nil {
			return review.ErrExecutionFailed
		}
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
	}
}
