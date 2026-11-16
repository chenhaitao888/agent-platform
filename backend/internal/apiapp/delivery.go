package apiapp

import (
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/strictjson"
	"context"
)

func loadArchives(ctx context.Context, filename, root string, catalog *servicecatalog.Catalog) (*delivery.Store, error) {
	if filename == "" {
		return nil, nil
	}
	if catalog == nil {
		return nil, delivery.ErrConfiguration
	}
	data, err := readPlanningFile(filename, root, 8192)
	if err != nil {
		return nil, delivery.ErrConfiguration
	}
	var config struct {
		URL string `json:"url"`
	}
	if strictjson.Decode(data, &config) != nil {
		return nil, delivery.ErrConfiguration
	}
	return delivery.Open(ctx, config.URL)
}
