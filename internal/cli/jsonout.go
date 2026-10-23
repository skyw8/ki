package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

// writeJSONLine prints one compact JSON document, matching the machine-readable
// output of the session commands (`--json`).
func writeJSONLine(out io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	_, err = fmt.Fprintf(out, "%s\n", b)
	return err
}
