package report

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"gh-inspect/internal/inspect"
)

//go:embed assets/graph.html
var graphTemplate string

const dataPlaceholder = "__GH_INSPECT_DATA__"

// WriteHTML writes a self-contained interactive report: a filterable network
// graph of repositories, users and teams, plus a searchable access table.
// It has no external dependencies, so it works offline.
func WriteHTML(w io.Writer, r *inspect.Report) error {
	// json.Marshal escapes <, > and & so the payload cannot close the
	// surrounding <script> element.
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if !strings.Contains(graphTemplate, dataPlaceholder) {
		return fmt.Errorf("html template is missing its data placeholder")
	}
	_, err = io.WriteString(w, strings.Replace(graphTemplate, dataPlaceholder, string(data), 1))
	return err
}
