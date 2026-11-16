package apiapp

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"agent-platform/backend/internal/httpapi"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/strictjson"
)

func loadServiceCatalog(filename, workspaceRoot string, authentication *httpapi.Authenticator, gitlabConfigured bool) (*servicecatalog.Catalog, error) {
	if filename == "" {
		return nil, nil
	}
	if !filepath.IsAbs(filename) || authentication == nil || !gitlabConfigured {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filename))
	if err != nil {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	filename = filepath.Join(parent, filepath.Base(filename))
	root, err := resolvedFutureDirectory(workspaceRoot)
	if err != nil {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	relative, err := filepath.Rel(root, filename)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	file, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() > 1<<20 {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	var config servicecatalog.Config
	if strictjson.Decode(data, &config) != nil {
		return nil, servicecatalog.ErrInvalidCatalog
	}
	return servicecatalog.New(config, authentication)
}
