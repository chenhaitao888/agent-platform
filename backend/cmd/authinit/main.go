// authinit creates local API credentials without printing raw tokens or changing
// a running API. It deliberately cannot overwrite or rotate an existing identity.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/backend/internal/httpapi"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("authinit", flag.ContinueOnError)
	// Invalid arguments may be mistaken credentials; never echo them.
	flags.SetOutput(io.Discard)
	directory := flags.String("dir", "", "new absolute credential directory; parent must exist")
	tenant := flags.String("tenant", "tenant-local", "tenant identity")
	subject := flags.String("subject", "local-developer", "subject identity")
	workspace := flags.String("workspace-root", strings.TrimSpace(os.Getenv("AGENT_PLATFORM_WORKSPACE_ROOT")), "workspace root to exclude; defaults to deployment environment")
	repositories := []string{}
	flags.Func("repository", "authorized GitLab project ID; repeat for multiple projects; omitted means no repository access", func(value string) error {
		repositories = append(repositories, value)
		return nil
	})
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, "Usage: authinit -dir /absolute/new-directory [-tenant tenant-local] [-subject local-developer] [-repository project-id ...] [-workspace-root /absolute/workspaces]\nCreates api-auth.json, api-token.txt and api-curl.conf with mode 0600 in a new 0700 directory. Parent must exist. Existing directories and Git/Workspace locations are refused. No repositories are authorized unless explicitly supplied. Raw tokens are never printed; configure AGENT_PLATFORM_AUTH_FILE and restart the API to use them.")
		return err
	} else if err != nil || flags.NArg() != 0 {
		return errors.New("invalid arguments; use authinit -help")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("cannot generate API credential")
	}
	token := hex.EncodeToString(random[:])
	digest := sha256.Sum256([]byte(token))
	credentials := []httpapi.Credential{{Subject: *subject, TenantID: *tenant, TokenSHA256: hex.EncodeToString(digest[:]), RepositoryIDs: repositories}}
	if _, err := httpapi.NewAuthenticator(credentials); err != nil {
		return errors.New("invalid subject, tenant or repository allowlist; use the API authentication configuration rules")
	}
	destination, err := credentialDirectory(*directory, *workspace)
	if err != nil {
		return err
	}
	config, err := json.MarshalIndent(struct {
		Identities []httpapi.Credential `json:"identities"`
	}{credentials}, "", "  ")
	if err != nil {
		return errors.New("cannot encode API authentication configuration")
	}
	// Mkdir reserves the whole bundle: repeated/concurrent invocations cannot mix
	// a new token with an old digest, even when the existing directory is empty.
	if err := os.Mkdir(destination, 0o700); err != nil {
		return errors.New("cannot create credential directory; it must not exist and its parent must be writable")
	}
	// An unusual umask may remove owner bits; restore only on our new directory.
	if err := os.Chmod(destination, 0o700); err != nil {
		return errors.New("cannot set private credential directory permissions")
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return errors.New("cannot open new credential directory")
	}
	defer func() { _ = root.Close() }()
	files := []struct{ name, content string }{
		{"api-token.txt", token + "\n"},
		{"api-curl.conf", "header = \"Authorization: Bearer " + token + "\"\n"},
		// Publish the server config last. On an I/O failure retain the private
		// directory for inspection; never delete or overwrite an existing bundle.
		{"api-auth.json", string(config) + "\n"},
	}
	for _, file := range files {
		if err := writePrivate(root, file.name, file.content); err != nil {
			return errors.New("credential initialization incomplete; private directory retained, use a new directory after resolving the filesystem error")
		}
	}
	if err := json.NewEncoder(output).Encode(struct {
		AuthFile      string `json:"authFile"`
		TokenFile     string `json:"tokenFile"`
		CurlConfig    string `json:"curlConfig"`
		RepositoryIDs int    `json:"repositoryCount"`
	}{filepath.Join(destination, "api-auth.json"), filepath.Join(destination, "api-token.txt"), filepath.Join(destination, "api-curl.conf"), len(repositories)}); err != nil {
		return errors.New("credentials created but summary could not be written; retain the existing directory")
	}
	return nil
}

func credentialDirectory(directory, workspace string) (string, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) == string(filepath.Separator) {
		return "", errors.New("-dir must be an absolute path to a new directory")
	}
	directory = filepath.Clean(directory)
	parent, err := filepath.EvalSymlinks(filepath.Dir(directory))
	if err != nil {
		return "", errors.New("credential parent directory must already exist and be accessible")
	}
	destination := filepath.Join(parent, filepath.Base(directory))
	for ancestor := destination; ; ancestor = filepath.Dir(ancestor) {
		if _, err := os.Lstat(filepath.Join(ancestor, ".git")); !os.IsNotExist(err) {
			return "", errors.New("credential directory must be outside Git checkouts")
		}
		if ancestor == filepath.Dir(ancestor) {
			break
		}
	}
	if workspace != "" {
		if !filepath.IsAbs(workspace) {
			return "", errors.New("workspace root must be absolute")
		}
		resolved, err := resolveFuturePath(filepath.Clean(workspace))
		if err != nil {
			return "", errors.New("cannot resolve workspace root")
		}
		relative, err := filepath.Rel(resolved, destination)
		if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return "", errors.New("credential directory must be outside the workspace root")
		}
	}
	return destination, nil
}

func resolveFuturePath(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err == nil || !os.IsNotExist(err) || filepath.Dir(name) == name {
		return resolved, err
	}
	parent, err := resolveFuturePath(filepath.Dir(name))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(name)), nil
}

func writePrivate(root *os.Root, name, content string) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := io.WriteString(file, content); err != nil {
		return err
	}
	return file.Close()
}
