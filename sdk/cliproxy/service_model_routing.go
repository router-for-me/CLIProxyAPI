package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

func (s *Service) modelRoutingState() modelrouting.ActiveStateV2 {
	if s == nil {
		return modelrouting.ActiveStateV2{}
	}
	s.modelRoutingStateMu.RLock()
	defer s.modelRoutingStateMu.RUnlock()
	if s.activeModelRouting == nil {
		return modelrouting.ActiveStateV2{}
	}
	copyIdentity := *s.activeModelRouting
	loadedAt := s.modelRoutingLoadedAt
	return modelrouting.ActiveStateV2{
		Identity:   &copyIdentity,
		LoadedAt:   &loadedAt,
		Projection: cloneModelRoutingProjection(s.activeModelRoutingConfig),
	}
}

func (s *Service) modelRoutingIdentity() *modelrouting.ActiveIdentityV2 {
	return s.modelRoutingState().Identity
}

func (s *Service) setModelRoutingState(identity *modelrouting.ActiveIdentityV2, projection *modelrouting.Config, loadedAt time.Time) {
	s.modelRoutingStateMu.Lock()
	defer s.modelRoutingStateMu.Unlock()
	if identity == nil {
		s.activeModelRouting = nil
		s.modelRoutingLoadedAt = time.Time{}
		s.activeModelRoutingConfig = nil
		return
	}
	copyIdentity := *identity
	s.activeModelRouting = &copyIdentity
	s.modelRoutingLoadedAt = loadedAt.UTC()
	s.activeModelRoutingConfig = cloneModelRoutingProjection(projection)
}

func cloneModelRoutingProjection(projection *modelrouting.Config) *modelrouting.Config {
	if projection == nil {
		return nil
	}
	return (&internalconfig.Config{ModelRouting: projection}).CloneForRuntime().ModelRouting
}

func cliproxyBinaryProvenance() modelrouting.BinaryProvenance {
	return modelrouting.BinaryProvenance{
		Version: buildinfo.Version,
		Commit:  buildinfo.Commit,
		BuiltAt: buildinfo.BuildDate,
	}
}

func routingSchemaInfo() modelrouting.RoutingSchemaInfo {
	return modelrouting.RoutingSchemaInfo{
		Version: modelrouting.SchemaVersion,
		Digest:  modelrouting.SchemaDigest(),
	}
}

func validatePublicationSecret(data []byte) error {
	var raw struct {
		RemoteManagement struct {
			SecretKey string `yaml:"secret-key"`
		} `yaml:"remote-management"`
	}
	if errYAML := yaml.Unmarshal(data, &raw); errYAML != nil {
		return fmt.Errorf("parse publication secret state: %w", errYAML)
	}
	secret := strings.TrimSpace(raw.RemoteManagement.SecretKey)
	if secret == "" || strings.HasPrefix(secret, "$2a$") || strings.HasPrefix(secret, "$2b$") || strings.HasPrefix(secret, "$2y$") {
		return nil
	}
	return fmt.Errorf("remote-management.secret-key must already be normalized before digest calculation")
}

func configWithoutModelRouting(cfg *internalconfig.Config) *internalconfig.Config {
	if cfg == nil {
		return nil
	}
	clone := cfg.CloneForRuntime()
	clone.ModelRouting = nil
	return clone
}

// publishModelRoutingConfig owns the complete CAS critical section from current
// identity read through durable bytes, runtime swap, and receipt creation.
func (s *Service) publishModelRoutingConfig(
	ctx context.Context,
	data []byte,
	expected *modelrouting.ActiveIdentityV2,
	bootstrap bool,
) (*internalconfig.Config, *modelrouting.ActivationReceiptV2, error) {
	if s == nil || s.coreManager == nil {
		return nil, nil, fmt.Errorf("publish model routing: service or auth manager is nil")
	}
	if ctx == nil {
		return nil, nil, fmt.Errorf("publish model routing: context is nil")
	}
	if errContext := ctx.Err(); errContext != nil {
		return nil, nil, errContext
	}
	finalBytes := append([]byte(nil), data...)
	if errSecret := validatePublicationSecret(finalBytes); errSecret != nil {
		return nil, nil, fmt.Errorf("%w: %v", modelrouting.ErrInvalidPublication, errSecret)
	}
	parsed, errParse := internalconfig.ParseConfigBytes(finalBytes)
	if errParse != nil {
		return nil, nil, fmt.Errorf("%w: parse config: %v", modelrouting.ErrInvalidPublication, errParse)
	}
	if parsed.ModelRouting == nil {
		return nil, nil, fmt.Errorf("%w: model-routing v2 is required", modelrouting.ErrInvalidPublication)
	}
	if bootstrap != (expected == nil) {
		return nil, nil, fmt.Errorf("%w: publication precondition mode is inconsistent", modelrouting.ErrCASConflict)
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	s.configRuntimeMu.Lock()
	defer s.configRuntimeMu.Unlock()
	if errContext := ctx.Err(); errContext != nil {
		return nil, nil, errContext
	}

	current := s.modelRoutingIdentity()
	if errTransition := modelrouting.ValidateTransition(current, expected, parsed.ModelRouting); errTransition != nil {
		return nil, nil, errTransition
	}
	s.cfgMu.RLock()
	currentConfig := s.cfg
	s.cfgMu.RUnlock()
	if !reflect.DeepEqual(configWithoutModelRouting(currentConfig), configWithoutModelRouting(parsed)) {
		return nil, nil, fmt.Errorf("%w: CAS publication may change only model-routing", modelrouting.ErrInvalidPublication)
	}
	prepared, errPrepare := s.coreManager.PrepareModelRouting(parsed.ModelRouting)
	if errPrepare != nil {
		return nil, nil, fmt.Errorf("%w: %v", modelrouting.ErrInvalidPublication, errPrepare)
	}
	identity := modelrouting.ActiveIdentityV2{
		Generation:       parsed.ModelRouting.Generation,
		SnapshotDigest:   parsed.ModelRouting.SnapshotDigest,
		ProjectionDigest: parsed.ModelRouting.ProjectionDigest,
		ConfigDigest:     modelrouting.ConfigDigest(finalBytes),
	}
	if errIdentity := identity.Validate(); errIdentity != nil {
		return nil, nil, fmt.Errorf("%w: %v", modelrouting.ErrInvalidPublication, errIdentity)
	}
	previousBytes, errRead := os.ReadFile(s.configPath)
	if errRead != nil {
		return nil, nil, fmt.Errorf("read active config before publication: %w", errRead)
	}
	if errWrite := internalconfig.WriteConfigAtomicExact(s.configPath, finalBytes); errWrite != nil {
		return nil, nil, fmt.Errorf("persist model-routing config: %w", errWrite)
	}
	if errActivate := s.coreManager.ActivatePreparedModelRouting(prepared); errActivate != nil {
		errRollback := internalconfig.WriteConfigAtomicExact(s.configPath, previousBytes)
		return nil, nil, errors.Join(
			fmt.Errorf("activate model-routing runtime: %w", errActivate),
			wrapOptionalError("restore config after activation failure", errRollback),
		)
	}

	loadedAt := time.Now().UTC()
	s.cfgMu.Lock()
	s.cfg = parsed
	s.cfgMu.Unlock()
	s.configSequence++
	s.setModelRoutingState(&identity, parsed.ModelRouting, loadedAt)
	managementasset.SetCurrentConfig(parsed)
	if s.watcher != nil {
		s.watcher.SetConfig(parsed)
	}
	return parsed.CloneForRuntime(), &modelrouting.ActivationReceiptV2{
		PreviousActive:   current,
		Active:           identity,
		RoutingSchema:    routingSchemaInfo(),
		BinaryProvenance: cliproxyBinaryProvenance(),
		LoadedAt:         loadedAt,
	}, nil
}

func wrapOptionalError(context string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", context, err)
}

func (s *Service) initializeModelRouting() error {
	if s == nil || s.coreManager == nil || s.cfg == nil {
		return fmt.Errorf("initialize model routing: service, auth manager, and config are required")
	}
	if s.cfg.ModelRouting == nil {
		prepared, errPrepare := s.coreManager.PrepareModelRouting(nil)
		if errPrepare != nil {
			return errPrepare
		}
		return s.coreManager.ActivatePreparedModelRouting(prepared)
	}
	data, errRead := os.ReadFile(s.configPath)
	if errRead != nil {
		return fmt.Errorf("read initial model-routing config: %w", errRead)
	}
	prepared, errPrepare := s.coreManager.PrepareModelRouting(s.cfg.ModelRouting)
	if errPrepare != nil {
		// A persisted projection the live credentials no longer satisfy (a
		// rotated or removed key) is refused, never served: it leaves the
		// config file and the running configuration, the proxy starts with no
		// active generation, the publisher bootstraps a fresh one through the
		// CAS endpoint, and every other route and config reload keeps working.
		log.Errorf("persisted model-routing generation %d is not activated: %v", s.cfg.ModelRouting.Generation, errPrepare)
		if errRemove := internalconfig.RemoveTopLevelKeyPreserveComments(s.configPath, "model-routing"); errRemove != nil {
			return fmt.Errorf("remove refused model-routing projection: %w", errRemove)
		}
		s.cfgMu.Lock()
		s.cfg = configWithoutModelRouting(s.cfg)
		s.cfgMu.Unlock()
		unrouted, errUnrouted := s.coreManager.PrepareModelRouting(nil)
		if errUnrouted != nil {
			return errUnrouted
		}
		if errActivate := s.coreManager.ActivatePreparedModelRouting(unrouted); errActivate != nil {
			return errActivate
		}
		s.setModelRoutingState(nil, nil, time.Time{})
		return nil
	}
	if errActivate := s.coreManager.ActivatePreparedModelRouting(prepared); errActivate != nil {
		return fmt.Errorf("activate initial model-routing runtime: %w", errActivate)
	}
	identity := &modelrouting.ActiveIdentityV2{
		Generation:       s.cfg.ModelRouting.Generation,
		SnapshotDigest:   s.cfg.ModelRouting.SnapshotDigest,
		ProjectionDigest: s.cfg.ModelRouting.ProjectionDigest,
		ConfigDigest:     modelrouting.ConfigDigest(data),
	}
	if errIdentity := identity.Validate(); errIdentity != nil {
		return fmt.Errorf("validate initial model-routing identity: %w", errIdentity)
	}
	s.setModelRoutingState(identity, s.cfg.ModelRouting, time.Now())
	return nil
}

func (s *Service) validateAppliedModelRouting(cfg *internalconfig.Config) error {
	active := s.modelRoutingIdentity()
	if cfg == nil || cfg.ModelRouting == nil {
		if active != nil {
			return fmt.Errorf("model-routing removal requires an impossible CAS transition")
		}
		return nil
	}
	if active == nil {
		return fmt.Errorf("model-routing changes require the Service-owned CAS publication endpoint")
	}
	if cfg.ModelRouting.Generation != active.Generation || cfg.ModelRouting.SnapshotDigest != active.SnapshotDigest || cfg.ModelRouting.ProjectionDigest != active.ProjectionDigest {
		return fmt.Errorf("model-routing changes require the Service-owned CAS publication endpoint")
	}
	s.cfgMu.RLock()
	current := s.cfg
	s.cfgMu.RUnlock()
	if current == nil || !reflect.DeepEqual(current, cfg) {
		return fmt.Errorf("active configuration changes require the Service-owned CAS publication endpoint")
	}
	data, errRead := os.ReadFile(s.configPath)
	if errRead != nil {
		return fmt.Errorf("read active config for digest verification: %w", errRead)
	}
	if digest := modelrouting.ConfigDigest(data); digest != active.ConfigDigest {
		return fmt.Errorf("active config bytes changed outside the Service-owned CAS publisher: got %s, want %s", digest, active.ConfigDigest)
	}
	return nil
}
