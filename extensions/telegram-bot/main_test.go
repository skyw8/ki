package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// telegramCall is one recorded Bot API request.
type telegramCall struct {
	method string
	params map[string]any
}

// fakeTelegram records the connector's requests and answers request number N
// with reply(method, N)'s raw Bot API body.
func fakeTelegram(t *testing.T, reply func(method string, ordinal int) string) (*httptest.Server, func() []telegramCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []telegramCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Errorf("decode request: %v", err)
		}
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		mu.Lock()
		calls = append(calls, telegramCall{method: method, params: params})
		ordinal := len(calls)
		mu.Unlock()
		_, _ = w.Write([]byte(reply(method, ordinal)))
	}))
	t.Cleanup(server.Close)
	return server, func() []telegramCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]telegramCall(nil), calls...)
	}
}

// groupOutput builds a connector whose final reply owns a group placeholder.
func groupOutput(t *testing.T, server *httptest.Server) (*telegramApp, *telegramWorker, lifecycleEvent, *outputState) {
	t.Helper()
	app := &telegramApp{ctx: context.Background(), outputs: map[string]*outputState{}, workers: map[string]*telegramWorker{}}
	worker := &telegramWorker{app: app, api: &botAPI{base: server.URL, token: "test", client: server.Client()}}
	ev := lifecycleEvent{
		RunID: "run-group", Role: "assistant", StopReason: "stop", Text: "完整回复",
		External: map[string]string{
			"connector": "telegram-bot", "accountId": "bot:1", "chatId": "42",
			"threadId": "0", "chatType": "supergroup",
		},
	}
	st := app.ensureOutput(ev, worker)
	st.text = ev.Text
	return app, worker, ev, st
}

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
	name, args, ok := parseSlash("/cd@ki_bot ./project")
	if !ok || name != "cd" || args != "./project" {
		t.Fatalf("parsed slash = %q %q %v", name, args, ok)
	}
	if _, _, ok := parseSlash("hello /cd"); ok {
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

func TestSessionThreadMergesOrdinaryGroupReplies(t *testing.T) {
	group := chat{ID: -1004449407453, Type: "supergroup", Title: "怀仁堂"}
	forum := chat{ID: -1004449407453, Type: "supergroup", Title: "怀仁堂", IsForum: true}
	private := chat{ID: 6164830811, Type: "private", FirstName: "bron2ebear"}
	cases := []struct {
		name             string
		chat             chat
		thread           int64
		botTopics, forum bool
		want             int64
	}{
		// Telegram creates a thread for every reply, but only a forum's topics are
		// conversations of their own.
		{"reply thread in an ordinary group", group, 145, false, false, 0},
		{"forum topic", forum, 145, false, true, 145},
		{"general topic", forum, 1, false, true, 0},
		{"group without a thread", group, 0, false, false, 0},
		{"private chat without threaded mode", private, 7, false, false, 0},
		{"private chat with BotFather threaded mode", private, 7, true, false, 7},
	}
	for _, tc := range cases {
		if got := sessionThread(tc.chat, tc.thread, tc.botTopics, tc.forum); got != tc.want {
			t.Errorf("%s: sessionThread = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestChatLabelNamesChatsInEnglish(t *testing.T) {
	group := chat{ID: -1004449407453, Type: "supergroup", Title: "怀仁堂"}
	cases := []struct {
		name   string
		chat   chat
		thread int64
		topic  string
		want   string
	}{
		{"group", group, 0, "", "group-怀仁堂"},
		{"group reply thread", group, 145, "", "group-怀仁堂-thread-145"},
		{"group forum topic", group, 145, "项目讨论", "group-怀仁堂-项目讨论"},
		{"private user name", chat{ID: 6164830811, Type: "private", FirstName: "bron2ebear"}, 0, "", "private-bron2ebear"},
		{"private username wins", chat{ID: 5, Type: "private", Username: "XueyuehuazZ", FirstName: "Yuheng"}, 0, "", "private-@XueyuehuazZ"},
		{"private without a name", chat{ID: 5241498812, Type: "private"}, 0, "", "private-5241498812"},
		{"long title with a newline", chat{ID: 6, Type: "supergroup", Title: "a\nb"}, 0, "", "group-a b"},
	}
	for _, tc := range cases {
		if got := chatLabel(tc.chat, tc.thread, tc.topic); got != tc.want {
			t.Errorf("%s: chatLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRepliesToBotCountsAsAddressed(t *testing.T) {
	me := user{ID: 42, Username: "ki_worker_bot"}
	if !repliesToBot(&message{ReplyTo: &message{From: &user{ID: 42, IsBot: true}}}, me) {
		t.Fatal("a reply to the bot's own message must count as addressed")
	}
	if repliesToBot(&message{ReplyTo: &message{From: &user{ID: 7}}}, me) {
		t.Fatal("a reply to another member must not count as addressed")
	}
	if repliesToBot(&message{ReplyTo: &message{}}, me) {
		t.Fatal("a reply without a sender must not count as addressed")
	}
	if repliesToBot(&message{}, me) {
		t.Fatal("a message without a reply must not count as addressed")
	}
	if repliesToBot(&message{ReplyTo: &message{From: &user{ID: 42}}}, user{}) {
		t.Fatal("an unresolved bot identity must not count as addressed")
	}
}

func TestGeneralTopicThreadIsNotSent(t *testing.T) {
	// Telegram answers message_thread_id=1 with "message thread not found": the
	// General topic is addressed by omitting the field.
	if _, ok := sendMessageParams(-100, 1, "hi")["message_thread_id"]; ok {
		t.Fatal("thread 1 must be omitted")
	}
	if _, ok := sendMessageParams(-100, 0, "hi")["message_thread_id"]; ok {
		t.Fatal("no thread must be omitted")
	}
	if got := sendMessageParams(-100, 145, "hi")["message_thread_id"]; got != int64(145) {
		t.Fatalf("topic thread = %v", got)
	}
}

func TestTopicNamesAreCachedForWorkspaceLabels(t *testing.T) {
	app := &telegramApp{
		ctx:       context.Background(),
		statePath: filepath.Join(t.TempDir(), "state.json"),
		state:     telegramState{Topics: map[string]string{}, Forums: map[string]bool{}},
		outputs:   map[string]*outputState{},
		workers:   map[string]*telegramWorker{},
	}
	worker := &telegramWorker{app: app, ctx: context.Background(), accountID: "bot:8733071196"}
	created := &message{
		MessageThreadID:   145,
		Chat:              chat{ID: -1004449407453, Type: "supergroup", Title: "怀仁堂"},
		ForumTopicCreated: &forumTopic{Name: "项目讨论"},
	}
	worker.rememberTopic(created)
	if got := worker.topicName(-1004449407453, 145); got != "项目讨论" {
		t.Fatalf("topic name = %q", got)
	}
	if got := worker.workspaceTitle(created.Chat, 145); got != "group-怀仁堂-项目讨论" {
		t.Fatalf("workspace title = %q", got)
	}
	// An icon-only rename carries no name and must keep the cached one.
	worker.rememberTopic(&message{MessageThreadID: 145, Chat: created.Chat, ForumTopicEdited: &forumTopic{}})
	if got := worker.topicName(-1004449407453, 145); got != "项目讨论" {
		t.Fatalf("topic name after icon edit = %q", got)
	}
	// A topic-less message must not invent an entry.
	worker.rememberTopic(&message{Chat: created.Chat, ForumTopicCreated: &forumTopic{Name: "General"}})
	if got := worker.topicName(-1004449407453, 0); got != "" {
		t.Fatalf("general topic name = %q", got)
	}
}

func TestFinalEditRetriesThrottlingAndIgnoresPacing(t *testing.T) {
	server, calls := fakeTelegram(t, func(method string, ordinal int) string {
		if method == "editMessageText" && ordinal == 1 {
			// Throttle answer without retry_after: the retry only depends on the
			// 429 code, and waiting a real second would only slow the test down.
			return `{"ok":false,"error_code":429,"description":"Too Many Requests: retry later"}`
		}
		return `{"ok":true,"result":{"message_id":7}}`
	})
	app, worker, ev, st := groupOutput(t, server)
	st.placeholderID = 99
	// A preview write just went out; the final answer must not be paced out.
	st.reserveWrite(time.Now())
	app.finishOutput(ev, worker)

	got := calls()
	if len(got) != 2 || got[0].method != "editMessageText" || got[1].method != "editMessageText" {
		t.Fatalf("telegram calls: %+v", got)
	}
	if text := got[1].params["text"]; text != "完整回复" {
		t.Fatalf("final edit text: %v", text)
	}
	if !st.final || st.text != "完整回复" {
		t.Fatalf("output state: %+v", st)
	}
}

func TestFailedFinalEditFallsBackToFreshMessage(t *testing.T) {
	server, calls := fakeTelegram(t, func(method string, _ int) string {
		if method == "editMessageText" {
			// A 4xx is not retried; the answer still has to reach the chat.
			return `{"ok":false,"error_code":400,"description":"message to edit not found"}`
		}
		return `{"ok":true,"result":{"message_id":7}}`
	})
	app, worker, ev, st := groupOutput(t, server)
	st.placeholderID = 99
	app.finishOutput(ev, worker)

	got := calls()
	if len(got) != 3 {
		t.Fatalf("telegram calls: %+v", got)
	}
	if got[0].method != "editMessageText" || got[1].method != "sendMessage" || got[2].method != "deleteMessage" {
		t.Fatalf("call order: %+v", got)
	}
	if text := got[1].params["text"]; text != "完整回复" {
		t.Fatalf("fallback text: %v", text)
	}
	if id := got[2].params["message_id"]; id != float64(99) {
		t.Fatalf("deleted message: %v", id)
	}
	if !st.final {
		t.Fatalf("output state: %+v", st)
	}
}

func TestPreviewWritesArePaced(t *testing.T) {
	now := time.Now()
	st := &outputState{}
	if !st.reserveWrite(now) {
		t.Fatal("a fresh output has no pacing to respect")
	}
	if st.reserveWrite(now.Add(minOutputWriteInterval / 2)) {
		t.Fatal("a preview inside the pacing window must be skipped")
	}
	if !st.reserveWrite(now.Add(minOutputWriteInterval)) {
		t.Fatal("a preview after the window must be allowed")
	}
	st.lastWriteAt = now
	if delay := st.outputDelay(now.Add(100 * time.Millisecond)); delay != minOutputWriteInterval-100*time.Millisecond {
		t.Fatalf("paced delay = %v", delay)
	}
	if delay := st.outputDelay(now.Add(2 * minOutputWriteInterval)); delay != outputDebounce {
		t.Fatalf("unpaced delay = %v", delay)
	}
	if delay := st.outputDelay(time.Time{}); delay != outputDebounce {
		t.Fatalf("zero-time delay = %v", delay)
	}
}
