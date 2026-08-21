package notionimport

import "gopkg.in/yaml.v3"

// marshalYAML marshals v to YAML bytes.
func marshalYAML(v any) ([]byte, error) {
	return yaml.Marshal(v)
}
