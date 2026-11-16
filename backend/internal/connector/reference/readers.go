// Package reference implements the versioned, GET-only planning reference
// protocol. It is a contract fixture adapter, not a company platform integration.
package reference

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/strictjson"
)

type Connection struct{ ID, Version, BaseURL, Token string }
type reader struct {
	connection Connection
	client     *http.Client
	descriptor planning.Descriptor
}
type ConfigReader struct{ reader }
type ExperimentReader struct{ reader }

func newReader(connection Connection, client *http.Client, kind string) (reader, error) {
	u, err := url.Parse(connection.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !planning.ValidIdentifier(connection.ID) || !planning.ValidIdentifier(connection.Version) || len(connection.Token) > 4096 || strings.ContainsAny(connection.Token, "\r\n\t ") {
		return reader{}, planning.ErrInvalid
	}
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout == 0 || copy.Timeout > 5*time.Second {
		copy.Timeout = 5 * time.Second
	}
	connection.BaseURL = strings.TrimRight(connection.BaseURL, "/")
	return reader{connection: connection, client: &copy, descriptor: planning.Descriptor{Kind: kind, ContractVersion: kind + "-read.v1", ImplementationVersion: "reference-http.v1", ConnectionVersion: connection.Version}}, nil
}
func NewConfigReader(c Connection, client *http.Client) (*ConfigReader, error) {
	r, e := newReader(c, client, "config")
	if e != nil {
		return nil, e
	}
	return &ConfigReader{r}, nil
}
func NewExperimentReader(c Connection, client *http.Client) (*ExperimentReader, error) {
	r, e := newReader(c, client, "experiment")
	if e != nil {
		return nil, e
	}
	return &ExperimentReader{r}, nil
}
func (r reader) Describe() planning.Descriptor { return r.descriptor }
func (r reader) read(ctx context.Context, target planning.Target, destination any) error {
	if target.ConnectorRef != r.connection.ID || !planning.ValidIdentifier(target.TenantID) || !planning.ValidIdentifier(target.Environment) || !planning.ValidIdentifier(target.ResourceRef) {
		return planning.ErrInvalid
	}
	query := url.Values{"tenantId": {target.TenantID}, "environment": {target.Environment}, "resourceRef": {target.ResourceRef}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.connection.BaseURL+"/v1/"+r.descriptor.Kind+"?"+query.Encode(), nil)
	if err != nil {
		return planning.ErrUnavailable
	}
	if r.connection.Token != "" {
		request.Header.Set("Authorization", "Bearer "+r.connection.Token)
	}
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return planning.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return planning.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || len(data) > 512<<10 {
		return planning.ErrUnavailable
	}
	if strictjson.Decode(data, destination) != nil {
		return planning.ErrUnavailable
	}
	return nil
}
func (r *ConfigReader) ReadConfig(ctx context.Context, target planning.Target) (planning.ConfigState, error) {
	var response struct {
		SchemaVersion string               `json:"schemaVersion"`
		State         planning.ConfigState `json:"state"`
	}
	if err := r.read(ctx, target, &response); err != nil {
		return planning.ConfigState{}, err
	}
	if response.SchemaVersion != r.descriptor.ContractVersion || !planning.ValidConfigState(response.State, target) {
		return planning.ConfigState{}, planning.ErrUnavailable
	}
	return response.State, nil
}
func (r *ExperimentReader) ReadExperiments(ctx context.Context, target planning.Target) (planning.ExperimentState, error) {
	var response struct {
		SchemaVersion string                   `json:"schemaVersion"`
		State         planning.ExperimentState `json:"state"`
	}
	if err := r.read(ctx, target, &response); err != nil {
		return planning.ExperimentState{}, err
	}
	if response.SchemaVersion != r.descriptor.ContractVersion || !planning.ValidExperimentState(response.State, target) {
		return planning.ExperimentState{}, planning.ErrUnavailable
	}
	return response.State, nil
}
