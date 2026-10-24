package question

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"ki/internal/session"
	toolapi "ki/internal/tool"
)

func arguments(t *testing.T, input string) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		t.Fatal(err)
	}
	return args
}

func TestQuestionValidation(t *testing.T) {
	tool := New(nil).(toolapi.Validator)
	for _, input := range []string{
		`{}`,
		`{"questions":null}`,
		`{"questions":[]}`,
		`{"questions":["title"]}`,
		`{"questions":[{}]}`,
		`{"questions":[{"title":""}]}`,
		`{"questions":[{"title":" \n\t"}]}`,
		`{"questions":[{"title":1}]}`,
		`{"questions":[{"title":"A","unknown":true}]}`,
		`{"questions":[{"title":"A","options":null}]}`,
		`{"questions":[{"title":"A","options":[]}]}`,
		`{"questions":[{"title":"A","options":[" "]}]}`,
		`{"questions":[{"title":"A","options":[1]}]}`,
		`{"questions":[{"title":"A"}],"blocking":true}`,
	} {
		t.Run(input, func(t *testing.T) {
			if err := tool.Validate(arguments(t, input)); err == nil {
				t.Fatal("invalid arguments accepted")
			}
		})
	}
	for _, input := range []string{
		`{"questions":[{"title":"What do you prefer?"}]}`,
		`{"questions":[{"title":"发布范围？","options":["内部","公开"]},{"title":"Anything else?"}]}`,
	} {
		if err := tool.Validate(arguments(t, input)); err != nil {
			t.Fatalf("valid arguments rejected: %v", err)
		}
	}
	if err := tool.Validate(map[string]any{"questions": []any{map[string]any{"title": strings.Repeat("x", MaxArgumentsBytes)}}}); err == nil {
		t.Fatal("oversized arguments accepted")
	}
}

func TestQuestionSchemaMatchesAsyncContract(t *testing.T) {
	spec := toolapi.SpecFor(New(nil))
	if spec.Type != "function" || spec.Description != Description {
		t.Fatalf("question declaration = %+v", spec)
	}
	if len(spec.Parameters["required"].([]any)) != 1 || spec.Parameters["additionalProperties"] != false {
		t.Fatalf("question schema = %+v", spec.Parameters)
	}
	properties := spec.Parameters["properties"].(map[string]any)
	if len(properties) != 1 || properties["questions"] == nil {
		t.Fatalf("extra schema knobs: %+v", properties)
	}
}

func TestQuestionAcceptancePreservesBatchAndIdentity(t *testing.T) {
	var gotCallID string
	var gotQuestions []session.QuestionInput
	tool := New(func(_ context.Context, callID string, questions []session.QuestionInput) error {
		gotCallID, gotQuestions = callID, questions
		return nil
	})
	ctx := toolapi.WithExecutionIdentity(context.Background(), toolapi.ExecutionIdentity{CallID: "call-123", RunID: "run-1"})
	args := arguments(t, `{"questions":[{"title":" A? ","options":["First","Second"]},{"title":"B?"}]}`)
	result := tool.Execute(ctx, args)
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text != `{"accepted":true}` {
		t.Fatalf("acceptance = %+v", result)
	}
	if gotCallID != "call-123" || !reflect.DeepEqual(gotQuestions, []session.QuestionInput{{Title: " A? ", Options: []string{"First", "Second"}}, {Title: "B?"}}) {
		t.Fatalf("request = %q %+v", gotCallID, gotQuestions)
	}
}

func TestQuestionFailuresDoNotCreatePartialBatch(t *testing.T) {
	calls := 0
	tool := New(func(context.Context, string, []session.QuestionInput) error {
		calls++
		return errors.New("persistence failed")
	})
	ctx := toolapi.WithExecutionIdentity(context.Background(), toolapi.ExecutionIdentity{CallID: "call-1"})
	if result := tool.Execute(ctx, arguments(t, `{"questions":[{"title":"valid"},{"title":" "}]}`)); !result.IsError || calls != 0 {
		t.Fatalf("invalid batch reached host: %+v calls=%d", result, calls)
	}
	if result := tool.Execute(context.Background(), arguments(t, `{"questions":[{"title":"valid"}]}`)); !result.IsError || calls != 0 {
		t.Fatalf("missing identity reached host: %+v calls=%d", result, calls)
	}
	if result := tool.Execute(ctx, arguments(t, `{"questions":[{"title":"valid"}]}`)); !result.IsError || calls != 1 || !strings.Contains(result.Content[0].Text, "persistence failed") {
		t.Fatalf("host failure lost: %+v calls=%d", result, calls)
	}
	if result := New(nil).Execute(ctx, arguments(t, `{"questions":[{"title":"valid"}]}`)); !result.IsError {
		t.Fatal("unavailable host accepted request")
	}
}
