package cli

import "encoding/json"

func printJSON(iostreams IO, v any) error {
	enc := json.NewEncoder(iostreams.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
