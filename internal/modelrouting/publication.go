package modelrouting

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// PreparePublication replaces only the routing resource and computes the identity
// to attest after activation. The service owns serialization and activation.
func PreparePublication(host, request []byte, current, expected *ActiveIdentityV2) ([]byte, *Config, ActiveIdentityV2, error) {
	var identity ActiveIdentityV2
	var incoming yaml.Node
	if err := yaml.Unmarshal(request, &incoming); err != nil {
		return nil, nil, identity, fmt.Errorf("%w: %w", ErrInvalidPublication, err)
	}
	if len(incoming.Content) != 1 || incoming.Content[0].Kind != yaml.MappingNode {
		return nil, nil, identity, ErrInvalidPublication
	}
	fields := incoming.Content[0].Content
	if len(fields) != 2 || fields[0].Value != "model-routing" || fields[1].Kind != yaml.MappingNode {
		return nil, nil, identity, fmt.Errorf("%w: expected only model-routing", ErrInvalidPublication)
	}
	var projection Config
	if err := fields[1].Decode(&projection); err != nil {
		return nil, nil, identity, fmt.Errorf("%w: %w", ErrInvalidPublication, err)
	}
	if err := projection.Validate(); err != nil {
		return nil, nil, identity, fmt.Errorf("%w: %w", ErrInvalidPublication, err)
	}
	if err := ValidateTransition(current, expected, &projection); err != nil {
		return nil, nil, identity, err
	}
	resource, err := RoutingYAML(&projection)
	if err != nil {
		return nil, nil, identity, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(host, &doc); err != nil {
		return nil, nil, identity, fmt.Errorf("parse host configuration: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, identity, fmt.Errorf("host configuration must be a mapping")
	}
	root := doc.Content[0]
	index := 0
	for ; index < len(root.Content); index += 2 {
		if root.Content[index].Value == "model-routing" {
			break
		}
	}
	if index == len(root.Content) {
		root.Content = append(root.Content, fields...)
	} else {
		root.Content[index+1] = fields[1]
	}
	data, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, nil, identity, err
	}
	identity = ActiveIdentityV2{
		Generation: projection.Generation, SnapshotDigest: projection.SnapshotDigest,
		ProjectionDigest: projection.ProjectionDigest, ConfigDigest: ConfigDigest(resource),
	}
	return data, &projection, identity, nil
}
