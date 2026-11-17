package reference

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"agent-platform/backend/internal/strictjson"
)

// WriteConnection is deployment-owned and separate from read-only Planning
// configuration. Targets are an exact allowlist, never supplied by a model or UI.
type WriteConnection struct {
	Connection
	Targets []planning.Target
}
type writer struct {
	reader     reader
	targets    map[planning.Target]bool
	descriptor planning.Descriptor
}
type ConfigWriter struct{ writer }
type ExperimentWriter struct{ writer }

var _ resourcewrite.Writer = (*ConfigWriter)(nil)
var _ resourcewrite.Writer = (*ExperimentWriter)(nil)

func NewConfigWriter(c WriteConnection, client *http.Client) (*ConfigWriter, error) {
	w, err := newWriter(c, client, "config")
	if err != nil {
		return nil, err
	}
	return &ConfigWriter{w}, nil
}
func NewExperimentWriter(c WriteConnection, client *http.Client) (*ExperimentWriter, error) {
	w, err := newWriter(c, client, "experiment")
	if err != nil {
		return nil, err
	}
	return &ExperimentWriter{w}, nil
}
func newWriter(c WriteConnection, client *http.Client, kind string) (writer, error) {
	if c.Token == "" || len(c.Targets) == 0 || len(c.Targets) > 64 {
		return writer{}, resourcewrite.ErrInvalid
	}
	safe, err := writeClient(client)
	if err != nil {
		return writer{}, err
	}
	r, err := newReader(c.Connection, safe, kind)
	if err != nil {
		return writer{}, resourcewrite.ErrInvalid
	}
	u, _ := url.Parse(c.BaseURL)
	if u.ForceQuery || u.Opaque != "" {
		return writer{}, resourcewrite.ErrInvalid
	}
	targets := map[planning.Target]bool{}
	for _, t := range c.Targets {
		if !resourcewrite.ValidTarget(t) || t.ConnectorRef != c.ID || targets[t] {
			return writer{}, resourcewrite.ErrInvalid
		}
		targets[t] = true
	}
	return writer{reader: r, targets: targets, descriptor: planning.Descriptor{Kind: kind, ContractVersion: kind + "-write.v1", ImplementationVersion: "reference-http.v1", ConnectionVersion: c.Version}}, nil
}
func writeClient(client *http.Client) (*http.Client, error) {
	var transport *http.Transport
	if client != nil && client.Transport != nil {
		var ok bool
		transport, ok = client.Transport.(*http.Transport)
		if !ok {
			return nil, resourcewrite.ErrInvalid
		}
	} else {
		transport = http.DefaultTransport.(*http.Transport)
	}
	transport = transport.Clone()
	//nolint:staticcheck // Legacy TLS dialers also bypass the configured TLS policy.
	if transport.DialTLS != nil || transport.DialTLSContext != nil {
		return nil, resourcewrite.ErrInvalid
	}
	transport.Proxy = nil
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		return nil, resourcewrite.ErrInvalid
	}
	transport.TLSClientConfig.ServerName = ""
	transport.TLSClientConfig.MinVersion = max(transport.TLSClientConfig.MinVersion, tls.VersionTLS12)
	timeout := 5 * time.Second
	if client != nil && client.Timeout > 0 && client.Timeout < timeout {
		timeout = client.Timeout
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func (w writer) Describe() planning.Descriptor { return w.descriptor }
func (w writer) accepts(i resourcewrite.Intent) bool {
	return i.Valid() && w.targets[i.Target] && i.Kind == w.descriptor.Kind && i.Writer == w.descriptor && i.Reader == w.reader.descriptor
}
func (w writer) Apply(ctx context.Context, i resourcewrite.Intent) (resourcewrite.Receipt, error) {
	if !w.accepts(i) {
		return resourcewrite.Receipt{}, resourcewrite.ErrInvalid
	}
	if !time.Now().Before(i.ExpiresAt) {
		return resourcewrite.Receipt{}, resourcewrite.ErrExpired
	}
	body, err := json.Marshal(i)
	if err != nil || len(body) > 64<<10 {
		return resourcewrite.Receipt{}, resourcewrite.ErrInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.reader.connection.BaseURL+"/v1/"+i.Kind+"/operations", bytes.NewReader(body))
	if err != nil {
		return resourcewrite.Receipt{}, resourcewrite.ErrInvalid
	}
	request.Header.Set("Content-Type", "application/json")
	// No GetBody: net/http must not automatically replay an uncertain POST.
	request.GetBody = nil
	request.Header.Set("Idempotency-Key", i.OperationID)
	return w.exchange(request, i)
}
func (w writer) Lookup(ctx context.Context, i resourcewrite.Intent) (resourcewrite.Receipt, error) {
	if !w.accepts(i) {
		return resourcewrite.Receipt{}, resourcewrite.ErrInvalid
	}
	query := url.Values{"tenantId": {i.Target.TenantID}, "environment": {i.Target.Environment}, "resourceRef": {i.Target.ResourceRef}, "operationId": {i.OperationID}, "intentHash": {i.Hash}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, w.reader.connection.BaseURL+"/v1/"+i.Kind+"/operations?"+query.Encode(), nil)
	if err != nil {
		return resourcewrite.Receipt{}, resourcewrite.ErrInvalid
	}
	return w.exchange(request, i)
}
func (w writer) exchange(request *http.Request, i resourcewrite.Intent) (resourcewrite.Receipt, error) {
	request.Header.Set("Authorization", "Bearer "+w.reader.connection.Token)
	request.Header.Set("Accept", "application/json")
	response, err := w.reader.client.Do(request)
	if err != nil {
		return resourcewrite.Receipt{}, resourcewrite.ErrUnknown
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return resourcewrite.Receipt{}, resourcewrite.ErrUnknown
	}
	if response.StatusCode == http.StatusConflict {
		var conflict struct {
			SchemaVersion string `json:"schemaVersion"`
			Code          string `json:"code"`
		}
		if strictjson.Decode(data, &conflict) == nil && conflict.SchemaVersion == "resource-write-error.v1" && conflict.Code == "idempotency_conflict" {
			return resourcewrite.Receipt{}, resourcewrite.ErrConflict
		}
	}
	if response.StatusCode != http.StatusOK {
		return resourcewrite.Receipt{}, resourcewrite.ErrUnknown
	}
	var receipt resourcewrite.Receipt
	if strictjson.Decode(data, &receipt) != nil || !receipt.ValidFor(i) {
		return resourcewrite.Receipt{}, resourcewrite.ErrUnknown
	}
	return receipt, nil
}
func (w writer) Verify(ctx context.Context, i resourcewrite.Intent, r resourcewrite.Receipt) (resourcewrite.Verification, error) {
	if !w.accepts(i) || !r.ValidFor(i) || r.Status != "APPLIED" {
		return resourcewrite.Verification{}, resourcewrite.ErrInvalid
	}
	if i.Kind == "config" {
		reader := ConfigReader{w.reader}
		state, err := reader.ReadConfig(ctx, i.Target)
		if err != nil {
			return resourcewrite.Verification{}, resourcewrite.ErrUnknown
		}
		return resourcewrite.VerifyConfig(i, r, state)
	}
	reader := ExperimentReader{w.reader}
	state, err := reader.ReadExperiments(ctx, i.Target)
	if err != nil {
		return resourcewrite.Verification{}, resourcewrite.ErrUnknown
	}
	return resourcewrite.VerifyExperiment(i, r, state)
}
