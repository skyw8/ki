package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestMentionsBotUsesTelegramEntities(t *testing.T) {
	me := user{ID: 42, Username: "ki_bot"}
	text := "你好 😀 @Ki_Bot 请处理"
	// Telegram offsets count UTF-16 code units: 你好(2) + space(1) + 😀(2) + space(1).
	mention := entity{Type: "mention", Offset: 6, Length: 7}
	if !mentionsBot(text, []entity{mention}, me) {
		t.Fatal("expected mention to match")
	}
	if got := stripBotMention(text, []entity{mention}, me); got != "你好 😀  请处理" {
		t.Fatalf("stripped mention = %q", got)
	}

	command := "/help@KI_BOT hello"
	commandEntity := entity{Type: "bot_command", Offset: 0, Length: 12}
	if !mentionsBot(command, []entity{commandEntity}, me) {
		t.Fatal("expected bot command mention to match")
	}
	if got := stripBotMention(command, []entity{commandEntity}, me); got != "/help hello" {
		t.Fatalf("stripped bot command = %q", got)
	}

	other := entity{Type: "text_mention", Offset: 0, Length: 5, User: &me}
	if !mentionsBot("Alice hello", []entity{other}, me) {
		t.Fatal("expected text_mention to match")
	}
}

func TestMentionsBotFallsBackToUsernameWhenEntitiesAreMissing(t *testing.T) {
	me := user{ID: 42, Username: "ki_worker_bot"}
	text := "请 @KI_WORKER_BOT, 处理这个问题"
	if !mentionsBot(text, nil, me) {
		t.Fatal("expected username fallback to match")
	}
	if got := stripBotMention(text, nil, me); got != "请 , 处理这个问题" {
		t.Fatalf("fallback mention was not stripped: %q", got)
	}
	if !mentionsBot("请@ki_worker_bot处理", nil, me) {
		t.Fatal("CJK-adjacent username fallback must match")
	}
	if mentionsBot("邮件地址 a@ki_worker_bot 不应触发", nil, me) {
		t.Fatal("email-like text must not count as a mention")
	}
}

func TestSplitTelegramByRunes(t *testing.T) {
	input := strings.Repeat("你", telegramMessageLimit+1)
	parts := splitTelegram(input)
	if len(parts) != 2 || len([]rune(parts[0])) != telegramMessageLimit || len([]rune(parts[1])) != 1 {
		t.Fatalf("parts = %d, lengths = %d/%d", len(parts), len([]rune(parts[0])), len([]rune(parts[1])))
	}
}

func TestTelegramPathComponentDoesNotEscape(t *testing.T) {
	for _, value := range []string{"../chat", "..", ".", "a/b", "-100123"} {
		got := safeComponent(value)
		if got == "" || got == "." || got == ".." || strings.ContainsAny(got, `/\\`) {
			t.Fatalf("unsafe component %q from %q", got, value)
		}
	}
}

func TestParseSlash(t *testing.T) {
	name, args, ok := parseSlash("/cwd@ki_bot ./project")
	if !ok || name != "cwd" || args != "./project" {
		t.Fatalf("parsed slash = %q %q %v", name, args, ok)
	}
	if _, _, ok := parseSlash("hello /cwd"); ok {
		t.Fatal("embedded slash must not be a command")
	}
}

func TestDeleteWebhookDropsPendingUpdates(t *testing.T) {
	var method string
	var params map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	api := &botAPI{base: server.URL, token: "test", client: server.Client()}
	if err := api.deleteWebhook(context.Background()); err != nil {
		t.Fatal(err)
	}
	if method != "deleteWebhook" || params["drop_pending_updates"] != true {
		t.Fatalf("deleteWebhook request: method=%q params=%v", method, params)
	}
}

func TestTextMessageIsNotDroppedWithoutAttachments(t *testing.T) {
	if !hasInput("hello", nil) {
		t.Fatal("text-only input must be accepted")
	}
	if hasInput("   ", nil) {
		t.Fatal("empty input without attachments must be ignored")
	}
}

func TestGroupReactionPolicy(t *testing.T) {
	if !shouldReact(false, false) {
		t.Fatal("private messages should be acknowledged")
	}
	if !shouldReact(true, true) {
		t.Fatal("addressed group messages should be acknowledged")
	}
	if shouldReact(true, false) {
		t.Fatal("ordinary group messages should not receive a reaction")
	}
}

func TestFailureReplacesGroupPlaceholderInsteadOfSendingPartialText(t *testing.T) {
	type call struct {
		method string
		params map[string]any
	}
	var mu sync.Mutex
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Errorf("decode request: %v", err)
		}
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		mu.Lock()
		calls = append(calls, call{method: method, params: params})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	app := &telegramApp{
		ctx:     context.Background(),
		outputs: map[string]*outputState{},
		workers: map[string]*telegramWorker{},
	}
	worker := &telegramWorker{app: app, api: &botAPI{base: server.URL, token: "test", client: server.Client()}}
	ev := lifecycleEvent{
		RunID:      "run-1",
		StopReason: "error", ErrorMessage: "Responses message output item has empty id",
		External: map[string]string{
			"connector": "telegram-bot", "accountId": "bot:1", "chatId": "42",
			"threadId": "0", "chatType": "group",
		},
	}
	st := app.ensureOutput(ev, worker)
	st.text = "pong"
	st.placeholderID = 99
	app.failOutput(ev, worker, "")

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].method != "editMessageText" {
		t.Fatalf("telegram calls: %+v", calls)
	}
	if got := calls[0].params["text"]; got != "⚠️ 模型请求失败：\nResponses message output item has empty id" {
		t.Fatalf("failure text: %v", got)
	}
	if strings.Contains(calls[0].params["text"].(string), "pong") {
		t.Fatal("partial model output leaked into failure message")
	}
	if !st.failed || st.text != "" {
		t.Fatalf("failure state: %+v", st)
	}
}

func TestFinishedOutputIgnoresLatePartialUpdate(t *testing.T) {
	type call struct{ method string }
	var mu sync.Mutex
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, call{method: r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	app := &telegramApp{ctx: context.Background(), outputs: map[string]*outputState{}, workers: map[string]*telegramWorker{}}
	worker := &telegramWorker{app: app, api: &botAPI{base: server.URL, token: "test", client: server.Client()}}
	finish := lifecycleEvent{
		RunID: "run-final", Role: "assistant", Text: "完整回复",
		External: map[string]string{"connector": "telegram-bot", "accountId": "bot:1", "chatId": "42", "threadId": "0", "chatType": "private"},
	}
	app.finishOutput(finish, worker)
	app.appendOutput(lifecycleEvent{RunID: finish.RunID, Role: "assistant", Text: "半截旧消息", External: finish.External}, worker)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].method != "sendMessage" {
		t.Fatalf("late update sent extra Telegram message: %+v", calls)
	}
	if st := app.output(finish.RunID); st == nil || !st.final || st.text != "完整回复" {
		t.Fatalf("late update mutated terminal state: %+v", st)
	}
}

func TestToolUseMessageEndWaitsForFinalAssistantTurn(t *testing.T) {
	type call struct {
		method string
		text   string
	}
	var mu sync.Mutex
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		mu.Lock()
		text, _ := params["text"].(string)
		calls = append(calls, call{
			method: r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:],
			text:   text,
		})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
	}))
	defer server.Close()

	app := &telegramApp{ctx: context.Background(), outputs: map[string]*outputState{}, workers: map[string]*telegramWorker{}}
	worker := &telegramWorker{app: app, api: &botAPI{base: server.URL, token: "test", client: server.Client()}}
	external := map[string]string{"connector": "telegram-bot", "accountId": "bot:1", "chatId": "42", "threadId": "0", "chatType": "private"}
	app.finishOutput(lifecycleEvent{RunID: "run-tool", Role: "assistant", StopReason: "toolUse", External: external}, worker)
	if st := app.output("run-tool"); st == nil || st.final {
		t.Fatalf("tool turn incorrectly finalized output: %+v", st)
	}
	app.finishOutput(lifecycleEvent{RunID: "run-tool", Role: "assistant", StopReason: "stop", Text: "最终回复", External: external}, worker)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].method != "sendMessage" || calls[0].text != "最终回复" {
		t.Fatalf("telegram calls: %+v", calls)
	}
}

func TestSettledOutputPersistsAccumulatedDraft(t *testing.T) {
	type call struct {
		method string
		text   string
	}
	var mu sync.Mutex
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		mu.Lock()
		text, _ := params["text"].(string)
		calls = append(calls, call{
			method: r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:],
			text:   text,
		})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":8}}`))
	}))
	defer server.Close()

	app := &telegramApp{ctx: context.Background(), outputs: map[string]*outputState{}, workers: map[string]*telegramWorker{}}
	worker := &telegramWorker{app: app, api: &botAPI{base: server.URL, token: "test", client: server.Client()}}
	ev := lifecycleEvent{RunID: "run-settled", Role: "assistant", External: map[string]string{
		"connector": "telegram-bot", "accountId": "bot:1", "chatId": "42", "threadId": "0", "chatType": "private",
	}}
	st := app.ensureOutput(ev, worker)
	st.text = "已经生成完的回复"
	st.draftID = 19
	app.settleOutput(ev.RunID)
	app.finalizeSettledOutput(ev.RunID, st)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].method != "sendMessage" || calls[0].text != "已经生成完的回复" {
		t.Fatalf("settled draft was not persisted: %+v", calls)
	}
	if !st.final || st.text == "" {
		t.Fatalf("settled output state: %+v", st)
	}
}

func TestTelegramFailureTextHasFallback(t *testing.T) {
	got := telegramFailureText(lifecycleEvent{})
	if got != "⚠️ 模型请求失败：\n模型请求失败，请稍后重试。" {
		t.Fatalf("fallback text: %q", got)
	}
}
