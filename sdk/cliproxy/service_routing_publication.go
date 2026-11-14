package cliproxy

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
)

func (s *Service) RoutingState() modelrouting.ActiveStateV2 {
	s.routingPublicationMu.Lock()
	defer s.routingPublicationMu.Unlock()
	return s.routingPublication
}

func (s *Service) RoutingInventory() (modelrouting.Inventory, error) {
	s.routingPublicationMu.Lock()
	defer s.routingPublicationMu.Unlock()
	return management.BuildRoutingInventory(s.coreManager, s.routingPublication)
}

func routingIdentity(projection *modelrouting.Config) (modelrouting.ActiveIdentityV2, error) {
	resource, err := modelrouting.RoutingYAML(projection)
	if err != nil {
		return modelrouting.ActiveIdentityV2{}, err
	}
	return modelrouting.ActiveIdentityV2{
		Generation: projection.Generation, SnapshotDigest: projection.SnapshotDigest,
		ProjectionDigest: projection.ProjectionDigest, ConfigDigest: modelrouting.ConfigDigest(resource),
	}, nil
}

func (s *Service) initializeRoutingPublication(projection *modelrouting.Config) error {
	if projection == nil {
		return nil
	}
	identity, err := routingIdentity(projection)
	if err != nil {
		return err
	}
	prepared, err := s.coreManager.PrepareModelRouting(projection)
	if err != nil {
		return fmt.Errorf("restore persisted model-routing generation: %w", err)
	}
	if err := s.coreManager.ActivatePreparedModelRouting(prepared); err != nil {
		return err
	}
	loaded := time.Now().UTC()
	s.routingPublication = modelrouting.ActiveStateV2{Identity: &identity, LoadedAt: &loaded, Projection: projection}
	return nil
}

func (s *Service) PublishRouting(ctx context.Context, request []byte, expected *modelrouting.ActiveIdentityV2) (modelrouting.ActivationReceiptV2, error) {
	s.routingPublicationMu.Lock()
	defer s.routingPublicationMu.Unlock()
	var receipt modelrouting.ActivationReceiptV2
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	host, err := os.ReadFile(s.configPath)
	if err != nil {
		return receipt, err
	}
	previous := s.routingPublication.Identity
	// Reject disk changes to the routing resource made outside the publisher.
	disk, err := config.ParseConfigBytes(host)
	if err != nil {
		return receipt, err
	}
	if (disk.ModelRouting == nil) != (previous == nil) {
		return receipt, modelrouting.ErrCASConflict
	}
	if previous != nil {
		identity, err := routingIdentity(disk.ModelRouting)
		if err != nil {
			return receipt, err
		}
		if identity != *previous {
			return receipt, modelrouting.ErrCASConflict
		}
	}
	data, projection, identity, err := modelrouting.PreparePublication(host, request, previous, expected)
	if err != nil {
		return receipt, err
	}
	prepared, err := s.coreManager.PrepareModelRouting(projection)
	if err != nil {
		return receipt, fmt.Errorf("%w: %w", modelrouting.ErrInvalidPublication, err)
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if err := config.WriteConfigAtomicExact(s.configPath, data); err != nil {
		return receipt, err
	}
	if err := s.coreManager.ActivatePreparedModelRouting(prepared); err != nil {
		return receipt, err
	}
	loaded := time.Now().UTC()
	s.routingPublication = modelrouting.ActiveStateV2{Identity: &identity, LoadedAt: &loaded, Projection: projection}
	return modelrouting.ActivationReceiptV2{
		PreviousActive: previous, Active: identity, LoadedAt: loaded,
		RoutingSchema:    modelrouting.RoutingSchemaInfo{Version: modelrouting.SchemaVersion, Digest: modelrouting.SchemaDigest()},
		BinaryProvenance: modelrouting.BinaryProvenance{Version: buildinfo.Version, Commit: buildinfo.Commit, BuiltAt: buildinfo.BuildDate},
	}, nil
}
