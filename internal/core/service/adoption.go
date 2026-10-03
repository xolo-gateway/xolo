package service

import (
	"context"
	"errors"
)

func (s *ProvisioningService) ExportAdoption(ctx context.Context) ([]byte, error) {
	exporter, ok := s.transactions.(interface {
		ExportAdoption(context.Context) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("adoption exporter unavailable")
	}
	return exporter.ExportAdoption(ctx)
}
