package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"go.yaml.in/yaml/v3"
)

// UpdateSOPS changes only recipient configuration, preserving existing rules,
// provider keys, YAML anchors and comments through yaml.Node. It never decrypts
// repository secret files or substitutes a homemade SOPS encryption format.
func UpdateSOPS(data []byte, change SOPSUpdate) ([]byte, error) {
	if !identifier.MatchString(change.AnchorValue) {
		return nil, errors.New("invalid SOPS anchor name")
	}
	if _, err := age.ParseX25519Recipient(change.Key); err != nil {
		return nil, errors.New("invalid SOPS public age key")
	}
	if len(change.Rules) == 0 {
		return nil, errors.New("at least one SOPS rule required")
	}
	var document yaml.Node
	if len(bytes.TrimSpace(data)) == 0 {
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	} else {
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&document); err != nil {
			return nil, err
		}
		if err := decoder.Decode(new(yaml.Node)); err != io.EOF {
			return nil, errors.New("SOPS configuration must contain one YAML document")
		}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("SOPS configuration must be a mapping")
	}
	root := document.Content[0]
	anchor := findAnchor(root, change.AnchorValue)
	if anchor != nil {
		if anchor.Kind != yaml.ScalarNode || !strings.HasPrefix(anchor.Value, "age1") {
			return nil, errors.New("SOPS anchor name already used for another value")
		}
		anchor.Value = change.Key
	} else {
		anchor = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: change.Key, Anchor: change.AnchorValue}
		collection := mappingValue(root, "keys")
		if collection == nil {
			collection = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			setMapping(root, "keys", collection)
		}
		if collection.Kind == yaml.ScalarNode {
			collection = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{collection}}
			setMapping(root, "keys", collection)
		}
		if collection.Kind != yaml.SequenceNode {
			return nil, errors.New("SOPS keys must be a sequence or anchored scalar")
		}
		collection.Content = append(collection.Content, anchor)
	}
	rules := mappingValue(root, "creation_rules")
	if rules == nil {
		rules = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setMapping(root, "creation_rules", rules)
	}
	if rules.Kind != yaml.SequenceNode {
		return nil, errors.New("SOPS creation_rules must be a sequence")
	}
	for _, pattern := range change.Rules {
		if pattern == "" {
			return nil, errors.New("SOPS rule path cannot be empty")
		}
		var rule *yaml.Node
		for _, existing := range rules.Content {
			if existing.Kind != yaml.MappingNode {
				return nil, errors.New("SOPS creation rule must be a mapping")
			}
			if path := mappingValue(existing, "path_regex"); path != nil && path.Value == pattern {
				rule = existing
				break
			}
		}
		if rule == nil {
			rule = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setMapping(rule, "path_regex", scalar(pattern))
			setMapping(rule, "key_groups", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}})
			rules.Content = append(rules.Content, rule)
		}
		groups := mappingValue(rule, "key_groups")
		if groups == nil {
			// Preserve top-level KMS/PGP settings instead of introducing key_groups,
			// which would cause SOPS to ignore those existing provider identities.
			if err := addAge(rule, anchor); err != nil {
				return nil, err
			}
			continue
		}
		if groups.Kind != yaml.SequenceNode || len(groups.Content) == 0 {
			return nil, errors.New("SOPS key_groups must be a nonempty sequence")
		}
		for _, group := range groups.Content {
			if group.Kind != yaml.MappingNode {
				return nil, errors.New("invalid SOPS key group")
			}
			if err := addAge(group, anchor); err != nil {
				return nil, err
			}
		}
	}
	// YAML aliases cannot refer forward. Put the public key declarations before
	// rules even when an existing configuration originally placed keys last.
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "keys" && i > 0 {
			pair := append([]*yaml.Node(nil), root.Content[i:i+2]...)
			rest := append(append([]*yaml.Node(nil), root.Content[:i]...), root.Content[i+2:]...)
			root.Content = append(pair, rest...)
			break
		}
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func setMapping(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, scalar(key), value)
}
func findAnchor(node *yaml.Node, name string) *yaml.Node {
	if node.Anchor == name {
		return node
	}
	for _, child := range node.Content {
		if found := findAnchor(child, name); found != nil {
			return found
		}
	}
	return nil
}
func addAge(group, anchor *yaml.Node) error {
	values := mappingValue(group, "age")
	if values == nil {
		values = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setMapping(group, "age", values)
	}
	if values.Kind == yaml.AliasNode {
		values = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{values}}
		setMapping(group, "age", values)
	}
	if values.Kind == yaml.ScalarNode {
		sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, key := range strings.Split(values.Value, ",") {
			if key = strings.TrimSpace(key); key != "" {
				sequence.Content = append(sequence.Content, scalar(key))
			}
		}
		values = sequence
		setMapping(group, "age", values)
	}
	if values.Kind != yaml.SequenceNode {
		return fmt.Errorf("SOPS age recipients must be text or a sequence")
	}
	for _, value := range values.Content {
		if value.Kind == yaml.AliasNode && value.Alias == anchor {
			return nil
		}
		if value.Kind == yaml.ScalarNode && value.Value == anchor.Value {
			return nil
		}
	}
	values.Content = append(values.Content, &yaml.Node{Kind: yaml.AliasNode, Value: anchor.Anchor, Alias: anchor})
	return nil
}
