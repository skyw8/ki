package provider

import (
	"encoding/json"
	"testing"

	"ki/internal/loop"
	"ki/internal/types"
)

func TestProtocolRequestCarriesResponsesCompactionState(t *testing.T) {
	raw := json.RawMessage(`{"type":"compaction","id":"cmp_1","encrypted_content":"opaque"}`)
	req := toProtocolRequest(loop.Request{
		Model:                     "gpt",
		ResponsesContext:          []json.RawMessage{raw},
		ResponsesCompactThreshold: 12345,
	})
	if req.ResponsesCompactThreshold != 12345 || len(req.ResponsesWindow) != 1 ||
		string(req.ResponsesWindow[0]) != string(raw) {
		t.Fatalf("protocol request: %+v", req)
	}
}

func TestResponsesCheckpointItemsIncludesPostCompactionAssistant(t *testing.T) {
	compaction := json.RawMessage(`{"type":"compaction","id":"cmp_1","encrypted_content":"opaque"}`)
	reasoning := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"reason"}`)
	answer := json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","future":"preserved","content":[{"type":"output_text","text":"answer"}]}`)
	items, err := ResponsesCheckpointItems(types.Message{
		Role:           "assistant",
		ResponsesItems: []json.RawMessage{compaction, reasoning, answer},
		Content:        []types.Content{{Type: "text", Text: "answer"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("checkpoint item count = %d: %s", len(items), items)
	}
	var typesInOrder []string
	for _, item := range items {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			t.Fatal(err)
		}
		typesInOrder = append(typesInOrder, header.Type)
	}
	if typesInOrder[0] != "compaction" || typesInOrder[1] != "reasoning" || typesInOrder[2] != "message" {
		t.Fatalf("checkpoint order: %v", typesInOrder)
	}
	if string(items[2]) != string(answer) {
		t.Fatalf("canonical message was reconstructed: %s", items[2])
	}
}

func TestBuiltinRemoteCompactionCapabilityIsExplicit(t *testing.T) {
	var openAI, xAI *Provider
	for _, candidate := range BuiltinProviders() {
		switch candidate.ID {
		case "openai":
			value := candidate
			openAI = &value
		case "xai":
			value := candidate
			xAI = &value
		}
	}
	if openAI == nil || len(openAI.Models) == 0 {
		t.Fatal("missing OpenAI catalog")
	}
	for _, model := range openAI.Models {
		if model.Compaction.Standalone != "openai" || model.Compaction.Inline != "openai" {
			t.Fatalf("OpenAI model %q capability = %+v", model.ID, model.Compaction)
		}
	}
	if xAI == nil {
		t.Fatal("missing xAI catalog")
	}
	for _, model := range xAI.Models {
		if model.API == "responses" && model.Compaction != (CompactionCapabilities{}) {
			t.Fatalf("compatible gateway inferred remote compaction: %+v", model)
		}
	}
}

func TestCredentialFingerprintChangesWithoutExposingSecret(t *testing.T) {
	first := CredentialFingerprint(Credential{Type: AuthAPIKey, APIKey: "secret-one"})
	second := CredentialFingerprint(Credential{Type: AuthAPIKey, APIKey: "secret-two"})
	if first == second || first == "" || len(first) != 32 {
		t.Fatalf("fingerprints: %q %q", first, second)
	}
	if first == "secret-one" || second == "secret-two" {
		t.Fatal("credential fingerprint exposed the credential")
	}
}

func TestCompactionCapabilitiesValidateByRuntimeOwner(t *testing.T) {
	native := Model{
		Provider: "native", ID: "m", API: "responses",
		BaseURL: "https://example.test", ContextWindow: 1000, MaxTokens: 100,
		Input:      []string{"text"},
		Compaction: CompactionCapabilities{Standalone: "codex-v2"},
	}
	if err := validateModel(native); err == nil {
		t.Fatal("native model accepted extension-owned codex-v2")
	}
	spec := ExtensionProviderSpec{
		ID: "codex", Name: "Codex", API: "codex-responses",
		BaseURL: "https://example.test", Auth: AuthSpec{Type: AuthOAuth},
		Models: []ModelSeed{{
			ID: "m", ContextWindow: 1000, MaxTokens: 100, Input: []string{"text"},
			Compaction: CompactionCapabilities{Standalone: "codex-v2"},
		}},
	}
	built, err := BuildExtensionProvider(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := built.Models[0].Compaction.Standalone; got != "codex-v2" {
		t.Fatalf("standalone capability = %q", got)
	}
}

func TestCompactionCapabilitiesAreIndependent(t *testing.T) {
	spec := ExtensionProviderSpec{
		ID: "hybrid", Name: "Hybrid", API: "responses",
		BaseURL: "https://example.test", Auth: AuthSpec{Type: AuthOAuth},
		Models: []ModelSeed{{
			ID: "m", ContextWindow: 1000, MaxTokens: 100, Input: []string{"text"},
			Compaction: CompactionCapabilities{Standalone: "codex-v2", Inline: "openai"},
		}},
	}
	built, err := BuildExtensionProvider(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := built.Models[0].Compaction; got != spec.Models[0].Compaction {
		t.Fatalf("capabilities collapsed: %+v", got)
	}
}

func TestCompactionOverrideCanClearBuiltinCapability(t *testing.T) {
	model := Model{Compaction: CompactionCapabilities{Standalone: "openai", Inline: "openai"}}
	empty := CompactionCapabilities{}
	got, err := applyModelOverride(model, ModelOverride{Compaction: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if got.Compaction != (CompactionCapabilities{}) {
		t.Fatalf("capability was not cleared: %+v", got.Compaction)
	}
}
