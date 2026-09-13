package main

import (
	"context"
	"time"

	"github.com/celikbros/kapsora/internal/platform/antivirus"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/health"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
)

// newReadiness probes the same document dependencies the API and worker require.
// The scanner is only pinged here; scanning remains exclusively in the worker.
func newReadiness(postgres health.Check, cfg config.DocumentConfig) (*health.Checker, error) {
	store, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint: cfg.Endpoint, Region: cfg.Region,
		AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey,
	})
	if err != nil {
		return nil, err
	}
	scanner, err := antivirus.NewClamd(antivirus.ClamdOptions{
		Address: cfg.ScannerAddr, Timeout: cfg.ScannerTimeout,
	})
	if err != nil {
		return nil, err
	}
	return health.NewChecker(2*time.Second).
		Add("postgresql", postgres).
		Add("objectstore.quarantine", func(ctx context.Context) error { return store.CheckBucket(ctx, cfg.QuarantineBucket) }).
		Add("objectstore.secure", func(ctx context.Context) error { return store.CheckBucket(ctx, cfg.SecureBucket) }).
		Add("clamd", scanner.Ping), nil
}
