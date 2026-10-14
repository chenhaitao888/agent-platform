package apiapp

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func deploymentHTTPClient(caFile string, timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		invalid := errors.New("deployment CA file must be an absolute regular PEM certificate file of at most 1 MiB")
		if !filepath.IsAbs(caFile) {
			return nil, invalid
		}
		file, err := os.OpenFile(caFile, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, invalid
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, invalid
		}
		content, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(content) > 1<<20 {
			return nil, invalid
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, invalid
		}
		if !roots.AppendCertsFromPEM(content) {
			return nil, invalid
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
