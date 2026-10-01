package tool

// Spec is the schema sent to the provider.
type Spec struct {
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Format      *Format        `json:"format,omitempty"`
}

// Format describes the grammar accepted by a Responses custom tool.
type Format struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

func SpecFor(t Tool) Spec {
	if p, ok := t.(SpecProvider); ok {
		spec := p.ToolSpec()
		spec.Name = MustCanonical(t.Name())
		return spec
	}
	return Spec{Type: "function", Name: MustCanonical(t.Name()), Description: t.Description() + "\n\n" + t.Prompt(), Parameters: t.Parameters()}
}
