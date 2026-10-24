package question

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ki/internal/session"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/tool/builtin/internal/support"
)

const MaxArgumentsBytes = 64 << 10

const Description = "Ask the user one or more questions during ongoing work. Use this tool only to request missing information, preferences, constraints, clarification, or approval. The tool returns immediately without ending the turn or waiting for a reply; any reply arrives asynchronously as a new user message. Keep questions concise, self-contained, and easy to understand, using a level of detail appropriate to the user and task. The UI always allows a free-text answer, including when suggested options are provided. A preselected option is not submitted automatically."

const Guidance = "When available, use `request_user_input_async` to ask for missing information, preferences, constraints, or clarification. Ask early and continue useful work that does not depend on the answer. Replies arrive as new user messages. For optional clarification, give the user a reasonable opportunity to reply before proceeding with a stated assumption. If an answer or approval is required, do not proceed with dependent work until it arrives. Elapsed time and preselected options are not answers or approval."

type RequestFunc func(context.Context, string, []session.QuestionInput) error

type questionTool struct {
	request RequestFunc
}

func New(request RequestFunc) toolapi.Tool { return &questionTool{request: request} }

func (*questionTool) Name() string        { return catalog.RequestUserInputAsync }
func (*questionTool) Description() string { return Description }
func (*questionTool) Prompt() string      { return "" }
func (*questionTool) Snippet() string {
	return "Ask questions without waiting; answers arrive as new user messages."
}

func (t *questionTool) ToolSpec() toolapi.Spec {
	return toolapi.Spec{Type: "function", Name: t.Name(), Description: Description, Parameters: t.Parameters()}
}

func (*questionTool) Parameters() map[string]any {
	return support.ObjectSchema([]any{"questions"}, map[string]any{
		"questions": map[string]any{
			"type": "array", "minItems": 1,
			"description": "One or more self-contained questions to present together, in display order.",
			"items": support.ObjectSchema([]any{"title"}, map[string]any{
				"title": map[string]any{
					"type": "string", "description": "The complete question shown to the user, including any context needed to answer it.",
				},
				"options": map[string]any{
					"type": "array", "minItems": 1,
					"description": "Suggested answers, in display order. Put the recommended answer first; the first option is preselected by default. The user can select one option or enter a free-text answer. Do not include an Other option or a free-text placeholder; the UI provides free-text input automatically. Omit options for a free-text-only question.",
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
	})
}

func (t *questionTool) Validate(args map[string]any) error {
	_, err := t.parse(args)
	return err
}

func (t *questionTool) parse(args map[string]any) ([]session.QuestionInput, error) {
	if err := support.ValidateArgs(t.Parameters(), t.Name(), args); err != nil {
		return nil, err
	}
	data, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("invalid question arguments: %w", err)
	}
	if len(data) > MaxArgumentsBytes {
		return nil, fmt.Errorf("question arguments exceed %d bytes", MaxArgumentsBytes)
	}
	raw, ok := args["questions"].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("questions must be a non-empty array")
	}
	questions := make([]session.QuestionInput, 0, len(raw))
	for index, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("questions[%d] must be an object", index)
		}
		for key := range item {
			if key != "title" && key != "options" {
				return nil, fmt.Errorf("questions[%d]: unknown property %q", index, key)
			}
		}
		title, ok := item["title"].(string)
		if !ok || strings.TrimSpace(title) == "" {
			return nil, fmt.Errorf("questions[%d].title must be a non-empty string", index)
		}
		q := session.QuestionInput{Title: title}
		if value, present := item["options"]; present {
			options, ok := value.([]any)
			if !ok || len(options) == 0 {
				return nil, fmt.Errorf("questions[%d].options must be a non-empty array", index)
			}
			for n, value := range options {
				option, ok := value.(string)
				if !ok || strings.TrimSpace(option) == "" {
					return nil, fmt.Errorf("questions[%d].options[%d] must be a non-empty string", index, n)
				}
				q.Options = append(q.Options, option)
			}
		}
		questions = append(questions, q)
	}
	return questions, nil
}

func (t *questionTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	questions, err := t.parse(args)
	if err != nil {
		return support.Error(err.Error())
	}
	if t.request == nil {
		return support.Error("user questions are unavailable in this session")
	}
	callID := toolapi.ExecutionFromContext(ctx).CallID
	if callID == "" {
		return support.Error("user questions require a tool call identity")
	}
	if err := t.request(ctx, callID, questions); err != nil {
		return support.Error(err.Error())
	}
	return support.Text(`{"accepted":true}`)
}
