package local

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// CardOptions are the repository-card fields `tf up` can set from flags.
type CardOptions struct {
	License     string
	Tags        []string
	Description string
	Title       string // "# Title" heading when a README is generated
}

// Empty reports whether no card field was given.
func (o CardOptions) Empty() bool {
	return o.License == "" && len(o.Tags) == 0 && o.Description == ""
}

// BuildReadme produces a README.md for a repository that has none locally:
//
//	---
//	license: mit
//	tags:
//	  - nlp
//	  - ja
//	description: Description
//	---
//
//	# Title
//
//	Description
//
// Keys that were not given are omitted from the front matter, and the front
// matter itself is omitted when no card field was given (an empty "---\n---"
// block is not recognised by the card parser). The description goes into the
// front matter -- the same `description` key MergeReadme maintains, so card
// consumers see it on a generated and a merged README alike -- and is
// repeated as the first paragraph of the body so the page reads naturally.
// Title falls back to "" (no heading).
func BuildReadme(opts CardOptions) []byte {
	var b strings.Builder
	// Only emit front matter when there is something to put in it: an empty
	// "---\n---" block is not recognised as a card by repocard.Parse (it wants
	// the closing fence on a later line) and would just be noise in the body.
	if !opts.Empty() {
		b.WriteString("---\n")
		if opts.License != "" {
			b.WriteString("license: " + yamlScalar(opts.License) + "\n")
		}
		if len(opts.Tags) > 0 {
			b.WriteString("tags:\n")
			for _, tag := range opts.Tags {
				b.WriteString("  - " + yamlScalar(tag) + "\n")
			}
		}
		if opts.Description != "" {
			b.WriteString("description: " + yamlScalar(opts.Description) + "\n")
		}
		b.WriteString("---\n")
	}

	var body []string
	if opts.Title != "" {
		body = append(body, "# "+opts.Title)
	}
	if opts.Description != "" {
		body = append(body, opts.Description)
	}
	if len(body) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(body, "\n\n"))
		b.WriteString("\n")
	}

	return []byte(b.String())
}

// yamlScalar renders a plain string as a YAML scalar, double-quoting it when
// it would otherwise be misread (leading/trailing space, indicator characters,
// something that parses as a number or boolean).
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	var probe any
	if err := yaml.Unmarshal([]byte(s), &probe); err == nil && s == strings.TrimSpace(s) {
		if str, ok := probe.(string); ok && str == s {
			return s
		}
	}
	quoted, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(quoted)
}

// MergeReadme updates the front matter of an existing README with the fields
// set in opts and returns the new content. Unset fields are left alone; tags
// are appended (deduplicated) to any existing list; the body and the order of
// the other keys are preserved (use yaml.v3 Nodes, not map marshalling). A
// README without front matter gets one prepended. Malformed YAML is an error.
func MergeReadme(existing []byte, opts CardOptions) ([]byte, error) {
	text := strings.ReplaceAll(string(existing), "\r\n", "\n")

	front, body := "", text
	if strings.HasPrefix(text, "---\n") {
		rest := text[len("---\n"):]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			front = rest[:end]
			body = strings.TrimPrefix(rest[end+len("\n---"):], "\n")
		}
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return nil, fmt.Errorf("local: parse front matter: %w", err)
	}

	var mapping *yaml.Node
	switch {
	case doc.Kind == 0:
		// No content at all (e.g. empty front matter): start a fresh mapping.
		mapping = &yaml.Node{Kind: yaml.MappingNode}
	case len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode:
		mapping = doc.Content[0]
	default:
		return nil, fmt.Errorf("local: front matter is not a YAML mapping")
	}

	if opts.License != "" {
		mappingSet(mapping, "license", opts.License)
	}
	if len(opts.Tags) > 0 {
		mappingAppendTags(mapping, opts.Tags)
	}
	if opts.Description != "" {
		mappingSet(mapping, "description", opts.Description)
	}

	var out bytes.Buffer
	out.WriteString("---\n")
	if len(mapping.Content) > 0 {
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		if err := enc.Encode(mapping); err != nil {
			return nil, fmt.Errorf("local: encode front matter: %w", err)
		}
		if err := enc.Close(); err != nil {
			return nil, fmt.Errorf("local: encode front matter: %w", err)
		}
	}
	out.WriteString("---\n")
	if body != "" {
		// body already carries its own leading blank line when it was split
		// off an existing front matter block; only synthesize one when we
		// prepended a fresh block in front of an unseparated body.
		if !strings.HasPrefix(body, "\n") {
			out.WriteString("\n")
		}
		out.WriteString(body)
	}

	return out.Bytes(), nil
}

// mappingGet returns the value node for key in a YAML mapping node, or nil.
func mappingGet(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// mappingSet sets key to a plain scalar string value, updating an existing
// entry in place (preserving its position) or appending a new one.
func mappingSet(mapping *yaml.Node, key, value string) {
	if v := mappingGet(mapping, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = ""
		v.Style = 0
		v.Value = value
		v.Content = nil
		v.Anchor = ""
		v.Alias = nil
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value},
	)
}

// mappingAppendTags appends tags (deduplicated against what's already there)
// to the "tags" sequence, creating it if absent or replacing it if it isn't
// already a sequence.
func mappingAppendTags(mapping *yaml.Node, tags []string) {
	var seq *yaml.Node
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != "tags" {
			continue
		}
		if mapping.Content[i+1].Kind == yaml.SequenceNode {
			seq = mapping.Content[i+1]
		} else {
			seq = &yaml.Node{Kind: yaml.SequenceNode}
			mapping.Content[i+1] = seq
		}
		break
	}
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode}
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "tags"},
			seq,
		)
	}

	existing := make(map[string]bool, len(seq.Content))
	for _, n := range seq.Content {
		existing[n.Value] = true
	}
	for _, tag := range tags {
		if existing[tag] {
			continue
		}
		existing[tag] = true
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: tag})
	}
}
