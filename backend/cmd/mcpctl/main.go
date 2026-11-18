// mcpctl is the deployment-owner entry point for company MCP onboarding.
// It only discovers tools, drafts/confirms bindings and probes configured reads.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/connector/mcpbridge"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/strictjson"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "MCP setup:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("use discover, suggest, confirm, check or probe; see docs/company-mcp-pilot.md")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	config := flags.String("config", "", "private MCP settings file")
	root := flags.String("workspace-root", "", "absolute Review workspace root (private files must be outside)")
	out := flags.String("out", "", "new private output file; existing files are never overwritten")
	draft := flags.String("draft", "", "proposal JSON to validate and confirm")
	by := flags.String("by", "", "operator identity for binding confirmation")
	expected := flags.String("expected-digest", "", "exact reviewed binding digest printed by check")
	model := flags.String("model-file", "", "private model connection file for suggest")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if !filepath.IsAbs(*root) || *config == "" || flags.NArg() != 0 {
		return errors.New("--config and absolute --workspace-root are required")
	}
	if command != "discover" && command != "suggest" && command != "confirm" && command != "check" && command != "probe" {
		return errors.New("unknown command")
	}
	s, err := apiapp.ReadMCPSettings(*config, *root)
	if err != nil {
		return err
	}
	reader, _, err := apiapp.MCPClient(s, *root, false)
	if err != nil {
		return err
	}
	writer, _, err := apiapp.MCPClient(s, *root, true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	readTools, err := reader.Discover(ctx)
	if err != nil {
		return err
	}
	writeTools, err := writer.Discover(ctx)
	if err != nil {
		return err
	}
	catalog, err := mergeCatalogs(readTools, writeTools)
	if err != nil {
		return err
	}
	if command == "discover" {
		hashes := map[string]string{}
		for _, tool := range catalog.Tools {
			hashes[tool.Name] = tool.Hash()
		}
		if err := writePrivate(*out, *root, struct {
			mcpbridge.Catalog
			ToolHashes map[string]string `json:"toolHashes"`
		}{catalog, hashes}); err != nil {
			return err
		}
		fmt.Printf("Discovered %d tools. Saved schemas to %s; no tools called.\n", len(catalog.Tools), *out)
		return nil
	}
	skill, err := apiapp.ReadMCPPrivateFile(s.SkillFile, *root, 64<<10)
	if err != nil {
		return err
	}
	if command == "suggest" {
		client, endpoint, token, name, err := modelClient(*model, *root)
		if err != nil {
			return err
		}
		proposal, err := mcpbridge.Suggest(ctx, client, endpoint, token, name, string(skill), catalog)
		if err != nil {
			return err
		}
		if err := writePrivate(*out, *root, proposal); err != nil {
			return err
		}
		fmt.Printf("Draft saved to %s; %d unresolved questions. Review it, then run check. No tools called.\n", *out, len(proposal.Questions))
		return nil
	}
	var binding mcpbridge.Binding
	if *draft != "" {
		data, err := apiapp.ReadMCPPrivateFile(*draft, *root, 1<<20)
		if err != nil {
			return err
		}
		var proposal mcpbridge.Proposal
		if strictjson.Decode(data, &proposal) != nil || proposal.Questions == nil || len(proposal.Questions) > 0 {
			return errors.New("proposal has unresolved questions or invalid fields; resolve capability gaps before confirming")
		}
		binding = proposal.Binding
	} else {
		data, err := apiapp.ReadMCPPrivateFile(s.BindingFile, *root, 1<<20)
		if err != nil {
			return err
		}
		binding, err = mcpbridge.ParseBinding(data)
		if err != nil {
			return err
		}
	}
	if binding.Validate(false) != nil || binding.SkillHash != planning.Hash(string(skill)) || binding.CheckCatalog(catalog) != nil {
		return errors.New("binding/Skill/tool schema changed or is incomplete; regenerate or update the proposal")
	}
	// Validate each identity's actual tool access, not just their union.
	readBinding := binding
	readBinding.Save = binding.Read
	readBinding.Submit = nil
	readBinding.LookupSubmit = nil
	if readBinding.CheckCatalog(readTools) != nil {
		return errors.New("read credentials cannot access the required discovery/query tools")
	}
	for _, call := range []mcpbridge.Call{binding.Save} {
		if !hasTool(writeTools, call) {
			return errors.New("write credentials cannot access the configured save tool")
		}
	}
	if binding.Submit != nil && !hasTool(writeTools, *binding.Submit) {
		return errors.New("write credentials cannot access the configured submit tool")
	}
	if binding.LookupSubmit != nil && !hasTool(readTools, *binding.LookupSubmit) {
		return errors.New("read credentials cannot access ticket lookup")
	}
	if command == "check" {
		fmt.Printf("Binding digest: %s\nSchema/access checks passed. Review arguments, response mappings, CAS/idempotency and company approval semantics before confirm. No tools called.\n", binding.Digest())
		return nil
	}
	if command == "confirm" {
		if *draft == "" || strings.TrimSpace(*by) == "" || *expected != binding.Digest() {
			return errors.New("confirm requires --draft, --by and the reviewed --expected-digest printed by check")
		}
		binding.Confirmation = &mcpbridge.Confirmation{Digest: binding.Digest(), By: *by, At: time.Now().UTC()}
		if err := writePrivate(*out, *root, binding); err != nil {
			return err
		}
		fmt.Printf("Confirmed binding saved to %s. Set bindingFile to this path and restart API/Worker. No tools called.\n", *out)
		return nil
	}
	if binding.Validate(true) != nil {
		return errors.New("probe requires a confirmed binding")
	}
	a := &mcpbridge.Adapter{ReadClient: reader, WriteClient: writer, Binding: binding, Targets: s.Targets}
	for _, target := range s.Targets {
		state, err := a.ReadConfig(ctx, target)
		if err != nil {
			return errors.New("configured read mapping failed; inspect the binding against your MCP response contract")
		}
		fmt.Printf("Read verified: %s/%s/%s, version %s, %d items (values omitted).\n", target.TenantID, target.Environment, target.ResourceRef, state.Version, len(state.Items))
	}
	return nil
}
func hasTool(c mcpbridge.Catalog, call mcpbridge.Call) bool {
	for _, t := range c.Tools {
		if t.Name == call.Tool && t.Hash() == call.ToolHash {
			return true
		}
	}
	return false
}
func mergeCatalogs(a, b mcpbridge.Catalog) (mcpbridge.Catalog, error) {
	seen := map[string]string{}
	out := mcpbridge.Catalog{Tools: []mcpbridge.Tool{}}
	for _, c := range []mcpbridge.Catalog{a, b} {
		for _, t := range c.Tools {
			if h, ok := seen[t.Name]; ok {
				if h != t.Hash() {
					return out, mcpbridge.ErrBinding
				}
				continue
			}
			seen[t.Name] = t.Hash()
			out.Tools = append(out.Tools, t)
		}
	}
	slices.SortFunc(out.Tools, func(a, b mcpbridge.Tool) int { return strings.Compare(a.Name, b.Name) })
	// Catalog digest binds the ordered discovered set, tool hashes bind calls.
	out.Digest = planning.Hash(out.Tools)
	return out, nil
}
func writePrivate(filename, root string, value any) error {
	if !filepath.IsAbs(filename) {
		return errors.New("--out must be an absolute new private file")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filename))
	if err != nil {
		return errors.New("create a private output directory first")
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(resolvedRoot); err == nil {
		resolvedRoot = real
	}
	rel, err := filepath.Rel(resolvedRoot, filepath.Join(parent, filepath.Base(filename)))
	if err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("output must be outside the Review workspace")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return mcpbridge.ErrBinding
	}
	f, err := os.OpenFile(filepath.Join(parent, filepath.Base(filename)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("output exists or cannot be created; choose a new versioned filename")
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("private output could not be saved")
	}
	return nil
}
func modelClient(filename, root string) (*http.Client, string, string, string, error) {
	var s struct {
		URL            string `json:"url"`
		Model          string `json:"model"`
		CredentialFile string `json:"credentialFile"`
		CAFile         string `json:"caFile,omitempty"`
	}
	data, err := apiapp.ReadMCPPrivateFile(filename, root, 16<<10)
	if err != nil || strictjson.Decode(data, &s) != nil {
		return nil, "", "", "", mcpbridge.ErrBinding
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/responses") || s.Model == "" {
		return nil, "", "", "", mcpbridge.ErrBinding
	}
	var credential struct {
		Token string `json:"token"`
	}
	data, err = apiapp.ReadMCPPrivateFile(s.CredentialFile, root, 8192)
	if err != nil || strictjson.Decode(data, &credential) != nil || strings.TrimSpace(credential.Token) == "" {
		return nil, "", "", "", mcpbridge.ErrBinding
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if s.CAFile != "" {
		ca, err := apiapp.ReadMCPPrivateFile(s.CAFile, root, 1<<20)
		if err != nil {
			return nil, "", "", "", mcpbridge.ErrBinding
		}
		roots, err := x509.SystemCertPool()
		if err != nil || !roots.AppendCertsFromPEM(ca) {
			return nil, "", "", "", mcpbridge.ErrBinding
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return mcpbridge.ErrConnection }}
	return client, s.URL, credential.Token, s.Model, nil
}
