package apiapp

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"agent-platform/backend/internal/httpapi"
)

func loadAuthentication(filename, workspaceRoot string) (*httpapi.Authenticator, error) {
	if filename == "" {
		return nil, nil
	}
	if !filepath.IsAbs(filename) {
		return nil, httpapi.ErrInvalidAuthentication
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filename))
	if err != nil {
		return nil, httpapi.ErrInvalidAuthentication
	}
	filename = filepath.Join(parent, filepath.Base(filename))
	if workspaceRoot != "" {
		// Resolve existing parents even when the workspace directory is not yet
		// created. Credential files may never be mounted into a Worker workspace.
		root, err := resolvedFutureDirectory(workspaceRoot)
		if err != nil {
			return nil, httpapi.ErrInvalidAuthentication
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return nil, httpapi.ErrInvalidAuthentication
		}
	}
	file, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, httpapi.ErrInvalidAuthentication
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 64<<10 {
		return nil, httpapi.ErrInvalidAuthentication
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, httpapi.ErrInvalidAuthentication
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, httpapi.ErrInvalidAuthentication
	}
	root, ok := authenticationObject(data, "identities")
	if !ok {
		return nil, httpapi.ErrInvalidAuthentication
	}
	var entries []json.RawMessage
	if json.Unmarshal(root["identities"], &entries) != nil || len(entries) == 0 || len(entries) > 64 {
		return nil, httpapi.ErrInvalidAuthentication
	}
	credentials := make([]httpapi.Credential, 0, len(entries))
	for _, entry := range entries {
		if _, ok := authenticationObject(entry, "subject", "tenantId", "tokenSha256", "repositoryIds"); !ok {
			return nil, httpapi.ErrInvalidAuthentication
		}
		var credential httpapi.Credential
		if json.Unmarshal(entry, &credential) != nil {
			return nil, httpapi.ErrInvalidAuthentication
		}
		credentials = append(credentials, credential)
	}
	return httpapi.NewAuthenticator(credentials)
}

func resolvedFutureDirectory(name string) (string, error) {
	if !filepath.IsAbs(name) {
		return "", httpapi.ErrInvalidAuthentication
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) || filepath.Dir(name) == name {
		return "", httpapi.ErrInvalidAuthentication
	}
	parent, err := resolvedFutureDirectory(filepath.Dir(name))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(name)), nil
}

func authenticationObject(data []byte, allowed ...string) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(data))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		permitted := false
		for _, name := range allowed {
			permitted = permitted || key == name
		}
		if _, duplicate := fields[key]; duplicate || !permitted {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
		fields[key] = value
	}
	closing, err := d.Token()
	return fields, err == nil && closing == json.Delim('}') && len(fields) == len(allowed) && d.Decode(new(any)) == io.EOF
}
