package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ki/internal/state"
)

const (
	stateFileName  = "state.json"
	configFileName = "config.json"
	// outputDebounce coalesces a burst of deltas into one preview write.
	outputDebounce = 250 * time.Millisecond
	// minOutputWriteInterval paces the writes to one chat. Telegram throttles
	// rapid chat writes, and a group reply is delivered by editing a placeholder
	// message: an unpaced preview turned a long answer into an edit storm, and
	// the throttled final edit then lost the answer.
	minOutputWriteInterval = time.Second
	// previewSendTimeout budgets one preview write (draft, placeholder, edit).
	previewSendTimeout = 15 * time.Second
	// finalSendTimeout budgets one final write, including a throttled retry. Each
	// part of a split answer gets its own budget, so a slow earlier part cannot
	// spend the deadline the remaining parts need.
	finalSendTimeout = 30 * time.Second
	// telegramRetryAttempts caps throttled (429) retries per Telegram call.
	telegramRetryAttempts = 2
	// telegramRetryMaxDelay caps one retry wait. The lifecycle goroutine writes
	// every session's replies in order, so a long server-side retry_after must
	// not stall it.
	telegramRetryMaxDelay = 10 * time.Second
	settleDelay           = 500 * time.Millisecond
	outputRetention       = 30 * time.Second
	secretValue           = "<configured>"
)

type telegramConfig struct {
	BotID          string `json:"botId"`
	Token          string `json:"token"`
	Model          string `json:"model"`
	ThinkingEffort string `json:"thinkingEffort"`
}

type telegramState struct {
	Version  int               `json:"version"`
	Offsets  map[string]int64  `json:"offsets"`
	Sessions map[string]string `json:"sessions"`
	// Topics caches forum topic names ("<chatId>:<threadId>") learned from topic
	// service messages; Telegram has no method to query a topic name.
	Topics map[string]string `json:"topics,omitempty"`
	// Forums caches the is_forum flag per chat, so a sidecar does not have to ask
	// getChat again for every message that carries a thread id.
	Forums map[string]bool `json:"forums,omitempty"`
}

type sessionCreateResult struct {
	SessionID   string         `json:"sessionId"`
	CWD         string         `json:"cwd"`
	WorkspaceID string         `json:"workspaceId"`
	Metadata    map[string]any `json:"metadata"`
	Provider    string         `json:"provider"`
	Model       string         `json:"model"`
}

type sessionSnapshot struct {
	ID       string         `json:"id"`
	CWD      string         `json:"cwd"`
	Metadata map[string]any `json:"metadata"`
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Thinking string         `json:"thinking"`
}

type enqueueResult struct {
	Accepted string `json:"accepted"`
	QueueID  string `json:"queueId"`
}

type lifecycleEvent struct {
	Type         string            `json:"type"`
	SessionID    string            `json:"sessionId"`
	Role         string            `json:"role"`
	ToolName     string            `json:"toolName"`
	ToolTitle    string            `json:"toolTitle"`
	RunID        string            `json:"runId"`
	Text         string            `json:"text"`
	StopReason   string            `json:"stopReason"`
	ErrorMessage string            `json:"errorMessage"`
	IsError      bool              `json:"isError"`
	Reason       string            `json:"reason"`
	External     map[string]string `json:"external"`
}

type lifecycleEnvelope struct {
	Event   string         `json:"event"`
	Payload lifecycleEvent `json:"payload"`
}

type telegramApp struct {
	rpc           *stdioRPC
	home          string
	extensionRoot string
	statePath     string
	ctx           context.Context
	cancel        context.CancelFunc
	workersMu     sync.Mutex
	workers       map[string]*telegramWorker
	stateMu       sync.Mutex
	state         telegramState
	outputMu      sync.Mutex
	outputs       map[string]*outputState
	lifecycle     chan lifecycleEnvelope
}

type telegramWorker struct {
	app       *telegramApp
	api       *botAPI
	ctx       context.Context
	cancel    context.CancelFunc
	me        user
	botID     int64
	accountID string
	model     string
	thinking  string
	draftSeq  atomic.Int64
}

type outputState struct {
	worker        *telegramWorker
	sendMu        sync.Mutex
	accountID     string
	chatID        int64
	threadID      int64
	private       bool
	external      map[string]string
	text          string
	timer         *time.Timer
	final         bool
	failed        bool
	settled       bool
	settleTimer   *time.Timer
	cleanup       *time.Timer
	draftID       int64
	draftOK       bool
	placeholderID int64
	statusID      int64
	// lastWriteAt paces this chat's writes at minOutputWriteInterval; guarded by
	// the app's outputMu.
	lastWriteAt time.Time
}

func newTelegramApp(rpc *stdioRPC) *telegramApp {
	home := os.Getenv("KI_HOME")
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".ki")
		}
	}
	root := cmp.Or(os.Getenv("KI_EXTENSION_ROOT"), filepath.Join(home, "extensions", "telegram-bot"))
	return &telegramApp{
		rpc:           rpc,
		home:          home,
		extensionRoot: root,
		statePath:     filepath.Join(root, stateFileName),
		workers:       map[string]*telegramWorker{},
		outputs:       map[string]*outputState{},
		lifecycle:     make(chan lifecycleEnvelope, 4096),
	}
}

func (a *telegramApp) handle(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		if err := a.initialize(); err != nil {
			return nil, err
		}
		return map[string]any{
			"tools":    []any{},
			"commands": []any{},
			"subscriptions": []map[string]string{
				{"event": "message_start", "mode": "async"},
				{"event": "message_update", "mode": "async"},
				{"event": "message_end", "mode": "async"},
				{"event": "tool_execution_start", "mode": "async"},
				{"event": "tool_execution_end", "mode": "async"},
				{"event": "agent_settled", "mode": "async"},
				{"event": "run_aborted", "mode": "async"},
				{"event": "queue_changed", "mode": "async"},
			},
		}, nil
	case "config.updated":
		go a.reloadConfig()
	case "lifecycle.event":
		var envelope lifecycleEnvelope
		if err := json.Unmarshal(raw, &envelope); err == nil {
			select {
			case a.lifecycle <- envelope:
			case <-a.ctx.Done():
			}
		}
	case "session.open", "session.close":
		// The connector keeps session identity in metadata and does not need a
		// per-session in-memory object. These notifications remain useful to
		// other extensions and are intentionally accepted here.
	case "shutdown":
		a.close()
	}
	return map[string]any{}, nil
}

func (a *telegramApp) initialize() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	go a.runLifecycle(a.ctx)
	state, err := loadTelegramState(a.statePath)
	if err != nil {
		return err
	}
	a.stateMu.Lock()
	a.state = state
	a.stateMu.Unlock()
	return a.reloadConfig()
}

func (a *telegramApp) runLifecycle(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case envelope := <-a.lifecycle:
			a.handleLifecycle(envelope.Event, envelope.Payload)
		}
	}
}

func (a *telegramApp) close() {
	if a.cancel != nil {
		a.cancel()
	}
	a.workersMu.Lock()
	for _, worker := range a.workers {
		worker.cancel()
	}
	a.workers = map[string]*telegramWorker{}
	a.workersMu.Unlock()
}

func (a *telegramApp) reloadConfig() error {
	config, err := loadTelegramConfig(filepath.Join(a.extensionRoot, configFileName))
	if err != nil {
		reportError("load config: " + err.Error())
		return err
	}
	a.workersMu.Lock()
	for _, worker := range a.workers {
		worker.cancel()
	}
	a.workers = map[string]*telegramWorker{}
	a.workersMu.Unlock()
	if strings.TrimSpace(config.Token) == "" || config.Token == secretValue || strings.TrimSpace(config.BotID) == "" {
		return nil
	}
	botID, err := strconv.ParseInt(strings.TrimSpace(config.BotID), 10, 64)
	if err != nil || botID <= 0 {
		err := fmt.Errorf("botId must be a positive integer")
		reportError("load config: " + err.Error())
		return err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	worker := &telegramWorker{
		app: a, api: newBotAPI(config.Token), ctx: ctx, cancel: cancel,
		botID: botID, accountID: "bot:" + strconv.FormatInt(botID, 10),
		model: strings.TrimSpace(config.Model), thinking: strings.TrimSpace(config.ThinkingEffort),
	}
	a.workersMu.Lock()
	a.workers[worker.accountID] = worker
	a.workersMu.Unlock()
	go worker.run()
	return nil
}

func loadTelegramConfig(path string) (telegramConfig, error) {
	cfg := telegramConfig{}
	b, _, err := state.ReadFile(path, 1, nil)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func loadTelegramState(path string) (telegramState, error) {
	saved := telegramState{
		Version:  1,
		Offsets:  map[string]int64{},
		Sessions: map[string]string{},
		Topics:   map[string]string{},
		Forums:   map[string]bool{},
	}
	b, _, err := state.ReadFile(path, 1, nil)
	if err != nil {
		if os.IsNotExist(err) {
			return saved, nil
		}
		return saved, err
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		return saved, err
	}
	if saved.Offsets == nil {
		saved.Offsets = map[string]int64{}
	}
	if saved.Sessions == nil {
		saved.Sessions = map[string]string{}
	}
	if saved.Topics == nil {
		saved.Topics = map[string]string{}
	}
	if saved.Forums == nil {
		saved.Forums = map[string]bool{}
	}
	return saved, nil
}

func (a *telegramApp) persistStateLocked() error {
	a.state.Version = 1
	// Versioned atomic writes keep polling offsets and session mappings intact
	// when the process stops mid-write, and never overwrite a newer schema.
	return state.WriteVersioned(a.statePath, 1, a.state, 0o600)
}

func (a *telegramApp) offset(accountID string) int64 {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.state.Offsets[accountID]
}

func (a *telegramApp) advance(accountID string, updateID int64) {
	a.stateMu.Lock()
	if next := updateID + 1; next > a.state.Offsets[accountID] {
		a.state.Offsets[accountID] = next
		if err := a.persistStateLocked(); err != nil {
			reportError("persist update offset: " + err.Error())
		}
	}
	a.stateMu.Unlock()
}

func (w *telegramWorker) run() {
	for {
		if w.ctx.Err() != nil {
			return
		}
		me, err := w.api.getMe(w.ctx)
		if err == nil {
			if err = w.api.deleteWebhook(w.ctx); err != nil {
				if !waitContext(w.ctx, 5*time.Second) {
					return
				}
				continue
			}
			w.me = me
			if me.ID != w.botID {
				reportError(fmt.Sprintf("configured botId %d does not match token owner %d", w.botID, me.ID))
				return
			}
			w.app.registerWorker(w)
			w.poll()
			return
		}
		if !waitContext(w.ctx, 5*time.Second) {
			return
		}
	}
}

func (a *telegramApp) registerWorker(worker *telegramWorker) {
	a.workersMu.Lock()
	a.workers[worker.accountID] = worker
	a.workersMu.Unlock()
}

func (a *telegramApp) worker(accountID string) *telegramWorker {
	a.workersMu.Lock()
	defer a.workersMu.Unlock()
	return a.workers[accountID]
}

func (w *telegramWorker) poll() {
	offset := w.app.offset(w.accountID)
	for {
		if w.ctx.Err() != nil {
			return
		}
		updates, err := w.api.getUpdates(w.ctx, offset)
		if err != nil {
			var apiErr *telegramError
			if errorsAs(err, &apiErr) && apiErr.RetryAfter > 0 {
				if !waitContext(w.ctx, time.Duration(apiErr.RetryAfter)*time.Second) {
					return
				}
			} else if !waitContext(w.ctx, 2*time.Second) {
				return
			}
			continue
		}
		for _, item := range updates {
			if item.UpdateID < offset {
				continue
			}
			if err := w.handleUpdate(item); err != nil {
				reportError(fmt.Sprintf("update %d: %v", item.UpdateID, err))
				continue
			}
			offset = item.UpdateID + 1
			w.app.advance(w.accountID, item.UpdateID)
		}
	}
}

func errorsAs(err error, target **telegramError) bool {
	value, ok := err.(*telegramError)
	if ok {
		*target = value
	}
	return ok
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (w *telegramWorker) handleUpdate(item update) error {
	msg := item.Message
	if msg == nil {
		return nil
	}
	// Topic service messages are the only place a topic name is ever revealed, so
	// they are recorded before anything else may drop the update.
	w.rememberTopic(msg)
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	group := isGroup(msg.Chat)
	text := msg.Text
	entities := msg.Entities
	if text == "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}
	// A reply to one of the bot's own messages addresses it just like a mention.
	addressed := !group || mentionsBot(text, entities, w.me) || repliesToBot(msg, w.me)
	if group && addressed {
		text = stripBotMention(text, entities, w.me)
	}
	if shouldReact(group, addressed) {
		// Ordinary Bot API updates have no user-client read marker. A reaction is
		// only an acknowledgement for private or explicitly addressed messages;
		// advancing the polling offset acknowledges an ordinary group message.
		reactionCtx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
		_ = w.api.setReaction(reactionCtx, msg.Chat.ID, msg.MessageID)
		cancel()
	}
	// Telegram enforces Managed Bot access restrictions. The mention only
	// controls whether a group message starts a run; all received group
	// messages are retained as context for the next addressed message.
	name, args, slash := parseSlash(text)
	control := slash && (name == "new" || name == "cd" || name == "compact" || name == "reload")

	thread := w.conversationThread(msg)
	key := w.externalKey(msg.Chat.ID, thread)
	sess, err := w.sessionFor(key, msg, thread)
	if err != nil {
		return err
	}
	contents, err := w.inputContents(text, msg, sess.CWD)
	if err != nil {
		return err
	}
	if !hasInput(text, contents) {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		text = "请查看附件。"
	}
	contents = append([]inputContent{{Type: "text", Text: authorPrefix(msg.From, text)}}, contents...)
	external := w.externalMetadata(msg, thread)
	if group && !addressed {
		ctx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
		var result map[string]any
		err := w.app.rpc.call(ctx, "session.appendMessage", map[string]any{
			"sessionId": sess.ID,
			"message": map[string]any{
				"role":     "user",
				"content":  contents,
				"origin":   "extension:telegram-bot",
				"external": external,
			},
			"idempotencyKey": w.accountID + ":" + strconv.FormatInt(item.UpdateID, 10),
		}, &result)
		cancel()
		return err
	}
	if err := w.applyModel(sess); err != nil {
		w.sendText(msg.Chat.ID, thread, "⚠️ Telegram 回复模型配置无效：\n"+err.Error())
		return nil
	}
	if control {
		return w.runCommand(name, args, key, sess, msg, thread)
	}

	var result enqueueResult
	ctx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
	err = w.app.rpc.call(ctx, "session.enqueue", map[string]any{
		"sessionId":      sess.ID,
		"content":        contents,
		"deliverAs":      "queue",
		"when":           "now",
		"idempotencyKey": w.accountID + ":" + strconv.FormatInt(item.UpdateID, 10),
		"kind":           "user",
		"external":       external,
	}, &result)
	cancel()
	return err
}

type inputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Path     string `json:"path,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Size     int64  `json:"size,omitzero"`
}

func hasInput(text string, contents []inputContent) bool {
	return len(contents) > 0 || strings.TrimSpace(text) != ""
}

func shouldReact(group, addressed bool) bool {
	return !group || addressed
}

func (w *telegramWorker) inputContents(text string, msg *message, cwd string) ([]inputContent, error) {
	var out []inputContent
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		path, err := w.downloadFile(photo.FileID, filepath.Join(cwd, ".telegram", strconv.FormatInt(msg.MessageID, 10)+".jpg"))
		if err != nil {
			return nil, err
		}
		out = append(out, inputContent{Type: "image", Path: path, MIMEType: "image/jpeg", Size: photo.FileSize})
	}
	if msg.Document != nil {
		name := msg.Document.FileName
		if name == "" {
			name = msg.Document.FileID + ".bin"
		}
		path, err := w.downloadFile(msg.Document.FileID, attachmentPath(cwd, name))
		if err != nil {
			return nil, err
		}
		kind := "file"
		if strings.HasPrefix(strings.ToLower(msg.Document.MIMEType), "image/") {
			kind = "image"
		}
		out = append(out, inputContent{Type: kind, Path: path, MIMEType: msg.Document.MIMEType, Size: msg.Document.FileSize})
	}
	if strings.TrimSpace(text) == "" && len(out) > 0 {
		text = "请查看附件。"
	}
	return out, nil
}

func (w *telegramWorker) downloadFile(fileID, target string) (string, error) {
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	info, err := w.api.getFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	// Failed or cancelled streams must not replace an existing attachment with
	// a partial file. Keep the temporary file beside the target for rename.
	err = w.api.download(ctx, info.FilePath, file)
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(file.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

func (w *telegramWorker) externalKey(chatID, threadID int64) string {
	return "telegram:" + w.accountID + ":" + strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(threadID, 10)
}

func (w *telegramWorker) externalMetadata(msg *message, threadID int64) map[string]string {
	return map[string]string{
		"source":      "telegram",
		"connector":   "telegram-bot",
		"accountId":   w.accountID,
		"externalKey": w.externalKey(msg.Chat.ID, threadID),
		"chatId":      strconv.FormatInt(msg.Chat.ID, 10),
		"threadId":    strconv.FormatInt(threadID, 10),
		"chatType":    msg.Chat.Type,
		"userId":      userID(msg.From),
		"messageId":   strconv.FormatInt(msg.MessageID, 10),
	}
}

// conversationThread is the thread id that identifies this message's
// conversation, plus one getChat probe per chat: replies create threads in every
// group, and only a forum's topics are conversations of their own.
func (w *telegramWorker) conversationThread(msg *message) int64 {
	forum := msg.Chat.IsForum
	if isGroup(msg.Chat) && !forum && msg.MessageThreadID > 1 {
		forum = w.forumChat(msg.Chat.ID)
	}
	return sessionThread(msg.Chat, msg.MessageThreadID, w.me.HasTopicsEnabled, forum)
}

// forumChat reports whether the chat is a forum, cached in state.json. Updates
// carry is_forum, but a sidecar started before the group gained topics would
// otherwise keep merging that forum's topics into one session.
func (w *telegramWorker) forumChat(chatID int64) bool {
	key := strconv.FormatInt(chatID, 10)
	w.app.stateMu.Lock()
	cached, known := w.app.state.Forums[key]
	w.app.stateMu.Unlock()
	if known {
		return cached
	}
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	got, err := w.api.getChat(ctx, chatID)
	cancel()
	if err != nil {
		// Keep the parent session until a later message can classify the chat;
		// guessing "forum" would fork a session out of an ordinary group.
		return false
	}
	w.app.stateMu.Lock()
	if w.app.state.Forums == nil {
		w.app.state.Forums = map[string]bool{}
	}
	w.app.state.Forums[key] = got.IsForum
	if err := w.app.persistStateLocked(); err != nil {
		reportError("persist forum flag: " + err.Error())
	}
	w.app.stateMu.Unlock()
	return got.IsForum
}

// rememberTopic caches the name of a forum topic. Telegram only reveals it in the
// forum_topic_created / forum_topic_edited service messages, and the label of the
// topic's workspace needs it.
func (w *telegramWorker) rememberTopic(msg *message) {
	name := ""
	switch {
	case msg.ForumTopicCreated != nil:
		name = msg.ForumTopicCreated.Name
	case msg.ForumTopicEdited != nil:
		// An edit may only change the icon, which carries no new name.
		name = msg.ForumTopicEdited.Name
	}
	if strings.TrimSpace(name) == "" || msg.MessageThreadID <= 1 {
		return
	}
	key := topicKey(msg.Chat.ID, msg.MessageThreadID)
	w.app.stateMu.Lock()
	if w.app.state.Topics == nil {
		w.app.state.Topics = map[string]string{}
	}
	if w.app.state.Topics[key] == name {
		w.app.stateMu.Unlock()
		return
	}
	w.app.state.Topics[key] = name
	if err := w.app.persistStateLocked(); err != nil {
		reportError("persist topic name: " + err.Error())
	}
	w.app.stateMu.Unlock()
}

func (w *telegramWorker) topicName(chatID, threadID int64) string {
	w.app.stateMu.Lock()
	defer w.app.stateMu.Unlock()
	return w.app.state.Topics[topicKey(chatID, threadID)]
}

func topicKey(chatID, threadID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(threadID, 10)
}

// workspaceTitle is the display name given to a workspace when the connector
// registers it: group-怀仁堂, group-怀仁堂-项目讨论, private-@ki_user.
func (w *telegramWorker) workspaceTitle(chat chat, threadID int64) string {
	return chatLabel(chat, threadID, w.topicName(chat.ID, threadID))
}

func (w *telegramWorker) sessionFor(key string, msg *message, threadID int64) (sessionSnapshot, error) {
	w.app.stateMu.Lock()
	id := w.app.state.Sessions[key]
	w.app.stateMu.Unlock()
	if id != "" {
		var got sessionSnapshot
		ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
		err := w.app.rpc.call(ctx, "session.get", map[string]any{"sessionId": id}, &got)
		cancel()
		if err == nil && got.ID != "" {
			return got, nil
		}
	}
	var listed struct {
		Sessions []sessionSnapshot `json:"sessions"`
	}
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	err := w.app.rpc.call(ctx, "session.list", map[string]any{
		"filter": map[string]any{"source": "telegram", "connector": "telegram-bot", "externalKey": key},
	}, &listed)
	cancel()
	if err == nil && len(listed.Sessions) > 0 {
		got := listed.Sessions[0]
		w.app.stateMu.Lock()
		w.app.state.Sessions[key] = got.ID
		_ = w.app.persistStateLocked()
		w.app.stateMu.Unlock()
		return got, nil
	}
	cwd := w.workspaceCWD(msg.Chat.ID, threadID)
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return sessionSnapshot{}, err
	}
	metadata := map[string]any{}
	for field, value := range w.externalMetadata(msg, threadID) {
		metadata[field] = value
	}
	var created sessionCreateResult
	ctx, cancel = context.WithTimeout(w.ctx, 15*time.Second)
	params := map[string]any{
		"cwd": cwd,
		// The workspace keeps this name until the user renames it in the WebUI:
		// session.create applies workspaceTitle only when the directory is
		// registered for the first time.
		"workspaceTitle": w.workspaceTitle(msg.Chat, threadID),
		"metadata":       metadata,
	}
	if w.model != "" {
		params["model"] = w.model
	}
	if w.thinking != "" {
		params["thinkingEffort"] = w.thinking
	}
	err = w.app.rpc.call(ctx, "session.create", params, &created)
	cancel()
	if err != nil {
		return sessionSnapshot{}, err
	}
	w.app.stateMu.Lock()
	w.app.state.Sessions[key] = created.SessionID
	if err := w.app.persistStateLocked(); err != nil {
		reportError("persist session mapping: " + err.Error())
	}
	w.app.stateMu.Unlock()
	return sessionSnapshot{ID: created.SessionID, CWD: created.CWD, Provider: created.Provider, Model: created.Model, Metadata: created.Metadata}, nil
}

func (w *telegramWorker) applyModel(sess sessionSnapshot) error {
	if sess.ID == "" {
		return nil
	}
	patch := map[string]any{}
	if w.model != "" && w.model != sess.Model && w.model != sess.Provider+"/"+sess.Model {
		patch["model"] = w.model
	}
	// The session keeps its model default while thinking is unset; only a
	// configured effort is pushed, and only when it differs.
	if w.thinking != "" && w.thinking != sess.Thinking {
		patch["thinkingEffort"] = w.thinking
	}
	if len(patch) == 0 {
		return nil
	}
	patch["sessionId"] = sess.ID
	ctx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
	defer cancel()
	return w.app.rpc.call(ctx, "session.patch", patch, nil)
}

func (w *telegramWorker) workspaceCWD(chatID, threadID int64) string {
	return filepath.Join(w.app.home, "workspace", "telegram", safeComponent(w.accountID), "chat-"+safeComponent(strconv.FormatInt(chatID, 10)), "topic-"+safeComponent(strconv.FormatInt(threadID, 10)))
}

func parseSlash(text string) (string, string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", "", false
	}
	name := strings.TrimPrefix(fields[0], "/")
	if at := strings.IndexByte(name, '@'); at >= 0 {
		name = name[:at]
	}
	if name == "" {
		return "", "", false
	}
	return strings.ToLower(name), strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0])), true
}

func authorPrefix(from *user, text string) string {
	return fmt.Sprintf("[Telegram 用户: %s, id=%s]\n%s", displayName(from), userID(from), text)
}

func (w *telegramWorker) runCommand(name, args, key string, sess sessionSnapshot, msg *message, threadID int64) error {
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	switch name {
	case "new":
		var result sessionCreateResult
		if err := w.app.rpc.call(ctx, "session.new", map[string]any{"sessionId": sess.ID}, &result); err != nil {
			w.sendText(msg.Chat.ID, threadID, "新会话创建失败："+err.Error())
			return nil
		}
		w.updateMapping(key, result.SessionID)
		w.sendText(msg.Chat.ID, threadID, "已开启新会话。")
	case "cd":
		if strings.TrimSpace(args) == "" {
			w.sendText(msg.Chat.ID, threadID, "用法：/cd <path>")
			return nil
		}
		path := strings.TrimSpace(args)
		if !filepath.IsAbs(path) {
			path = filepath.Join(sess.CWD, path)
		}
		path, err := filepath.Abs(path)
		if err != nil {
			w.sendText(msg.Chat.ID, threadID, "工作目录无效："+err.Error())
			return nil
		}
		var result sessionCreateResult
		if err := w.app.rpc.call(ctx, "session.new", map[string]any{"sessionId": sess.ID, "cwd": path}, &result); err != nil {
			w.sendText(msg.Chat.ID, threadID, "切换工作目录失败："+err.Error())
			return nil
		}
		w.updateMapping(key, result.SessionID)
		w.sendText(msg.Chat.ID, threadID, "工作目录已切换到：\n"+result.CWD)
	case "compact":
		if err := w.app.rpc.call(ctx, "session.compact", map[string]any{"sessionId": sess.ID}, nil); err != nil {
			w.sendText(msg.Chat.ID, threadID, "压缩失败："+err.Error())
			return nil
		}
		w.sendText(msg.Chat.ID, threadID, "会话已压缩。")
	case "reload":
		if err := w.app.rpc.call(ctx, "session.reload", map[string]any{"sessionId": sess.ID}, nil); err != nil {
			w.sendText(msg.Chat.ID, threadID, "重载失败："+err.Error())
			return nil
		}
		w.sendText(msg.Chat.ID, threadID, "会话扩展资源已重载。")
	}
	return nil
}

func (w *telegramWorker) updateMapping(key, id string) {
	if id == "" {
		return
	}
	w.app.stateMu.Lock()
	w.app.state.Sessions[key] = id
	if err := w.app.persistStateLocked(); err != nil {
		reportError("persist session mapping: " + err.Error())
	}
	w.app.stateMu.Unlock()
}

func (w *telegramWorker) sendText(chatID, threadID int64, text string) {
	for _, part := range splitTelegram(text) {
		ctx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
		if _, err := w.api.sendMessage(ctx, chatID, threadID, part); err != nil {
			reportError("send message: " + err.Error())
		}
		cancel()
	}
}

func (a *telegramApp) handleLifecycle(event string, ev lifecycleEvent) {
	if ev.External == nil || ev.External["connector"] != "telegram-bot" {
		return
	}
	if ev.RunID == "" {
		return
	}
	w := a.worker(ev.External["accountId"])
	if w == nil {
		return
	}
	switch event {
	case "message_start":
		if ev.Role != "assistant" {
			return
		}
		a.beginOutput(ev, w)
	case "message_update":
		if ev.Role != "assistant" {
			return
		}
		a.appendOutput(ev, w)
	case "message_end":
		if ev.Role != "assistant" {
			return
		}
		if ev.IsError || ev.StopReason == "error" || ev.StopReason == "aborted" || strings.TrimSpace(ev.ErrorMessage) != "" {
			override := ""
			if ev.StopReason == "aborted" && strings.TrimSpace(ev.ErrorMessage) == "" {
				override = "⚠️ 本次运行已停止。"
			}
			a.failOutput(ev, w, override)
			return
		}
		a.finishOutput(ev, w)
	case "tool_execution_start":
		a.sendToolStatus(ev, w)
	case "run_aborted":
		a.failOutput(ev, w, "⚠️ 本次运行已停止。")
	case "agent_settled":
		a.settleOutput(ev.RunID)
	}
}

func (a *telegramApp) settleOutput(runID string) {
	a.outputMu.Lock()
	st := a.outputs[runID]
	if st == nil {
		a.outputMu.Unlock()
		return
	}
	// Keep accumulated text until it has been persisted with sendMessage.
	// settle is a fallback terminal signal: message_end normally finalizes first,
	// while the delayed fallback covers a missing or formerly reordered end.
	st.settled = true
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	if st.final || st.failed {
		a.outputMu.Unlock()
		a.scheduleOutputCleanup(runID, st)
		return
	}
	if st.settleTimer != nil {
		st.settleTimer.Stop()
	}
	st.settleTimer = time.AfterFunc(settleDelay, func() { a.finalizeSettledOutput(runID, st) })
	a.outputMu.Unlock()
}

func (a *telegramApp) finalizeSettledOutput(runID string, st *outputState) {
	a.outputMu.Lock()
	if a.outputs[runID] != st || st.final || st.failed {
		a.outputMu.Unlock()
		return
	}
	st.settleTimer = nil
	if strings.TrimSpace(st.text) == "" {
		st.final = true
		a.outputMu.Unlock()
		a.scheduleOutputCleanup(runID, st)
		return
	}
	st.final = true
	a.outputMu.Unlock()
	a.flushOutput(runID, true)
}

func (a *telegramApp) beginOutput(ev lifecycleEvent, w *telegramWorker) *outputState {
	st := a.ensureOutput(ev, w)
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if st == nil || st.final || st.failed {
		return st
	}
	// A retry starts a fresh assistant attempt under the same run ID. Clear
	// the previous partial so a transient failure cannot concatenate replies.
	if st.text != "" {
		st.text = ""
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
	}
	return st
}

func (a *telegramApp) ensureOutput(ev lifecycleEvent, w *telegramWorker) *outputState {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if st := a.outputs[ev.RunID]; st != nil {
		return st
	}
	st := &outputState{
		worker: w, accountID: ev.External["accountId"], chatID: int64Value(ev.External["chatId"]),
		threadID: int64Value(ev.External["threadId"]), private: ev.External["chatType"] == "private",
		external: maps.Clone(ev.External), draftOK: true,
	}
	a.outputs[ev.RunID] = st
	return st
}

func (a *telegramApp) appendOutput(ev lifecycleEvent, w *telegramWorker) {
	st := a.ensureOutput(ev, w)
	a.outputMu.Lock()
	if st != nil && !st.final && !st.failed {
		st.text += ev.Text
		if st.timer == nil {
			st.timer = time.AfterFunc(st.outputDelay(time.Now()), func() { a.flushOutput(ev.RunID, false) })
		}
	}
	a.outputMu.Unlock()
}

// reserveWrite reports whether a preview write may reach Telegram now, and
// records the attempt when it may. Telegram throttles per-chat writes, so a
// preview that would land inside the pacing window is skipped: the next delta
// schedules another tick, and the final write is never paced. Must be called
// with outputMu held.
func (st *outputState) reserveWrite(now time.Time) bool {
	if now.Sub(st.lastWriteAt) < minOutputWriteInterval {
		return false
	}
	st.lastWriteAt = now
	return true
}

// outputDelay returns how long to wait before the pending preview write: never
// inside the pacing window, and at least outputDebounce after the newest delta.
// Deferring the timer, instead of writing on every delta, keeps the placeholder
// rendering without spending the chat's whole write budget on one answer. Must
// be called with outputMu held.
func (st *outputState) outputDelay(now time.Time) time.Duration {
	if wait := minOutputWriteInterval - now.Sub(st.lastWriteAt); wait > outputDebounce {
		return wait
	}
	return outputDebounce
}

func (a *telegramApp) finishOutput(ev lifecycleEvent, w *telegramWorker) {
	st := a.ensureOutput(ev, w)
	a.outputMu.Lock()
	if st == nil || st.failed || st.final {
		a.outputMu.Unlock()
		return
	}
	if strings.TrimSpace(ev.Text) != "" {
		st.text = ev.Text
	}
	if ev.StopReason == "toolUse" {
		// message_end is emitted for every assistant turn. A tool call ends one
		// turn, not the run; the later assistant turn owns the durable reply.
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
		a.outputMu.Unlock()
		return
	}
	if st.settleTimer != nil {
		st.settleTimer.Stop()
		st.settleTimer = nil
	}
	if strings.TrimSpace(st.text) == "" {
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
		st.final = true
		statusID := st.statusID
		chatID := st.chatID
		worker := st.worker
		a.outputMu.Unlock()
		if statusID != 0 {
			ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
			_ = worker.api.deleteMessage(ctx, chatID, statusID)
			cancel()
		}
		a.scheduleOutputCleanup(ev.RunID, st)
		return
	}
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.final = true
	a.outputMu.Unlock()
	a.flushOutput(ev.RunID, true)
}

func (a *telegramApp) failOutput(ev lifecycleEvent, w *telegramWorker, override string) {
	st := a.ensureOutput(ev, w)
	if st == nil {
		return
	}
	text := cmp.Or(override, telegramFailureText(ev))
	a.outputMu.Lock()
	if st.failed || st.final {
		a.outputMu.Unlock()
		return
	}
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	if st.settleTimer != nil {
		st.settleTimer.Stop()
		st.settleTimer = nil
	}
	st.text = ""
	st.final = true
	st.failed = true
	private, draftID, draftOK := st.private, st.draftID, st.draftOK
	placeholderID, statusID := st.placeholderID, st.statusID
	chatID, threadID := st.chatID, st.threadID
	worker := st.worker
	a.outputMu.Unlock()

	st.sendMu.Lock()
	defer st.sendMu.Unlock()
	ctx, cancel := context.WithTimeout(a.ctx, previewSendTimeout)
	defer cancel()
	if private && draftOK && draftID != 0 {
		// Replace a private draft before sending the durable error message.
		// The final send also closes the draft on Telegram clients that support
		// sendMessageDraft.
		_ = worker.api.sendMessageDraft(ctx, chatID, threadID, draftID, text)
	}
	// The notice explaining why there is no answer is a terminal write: it
	// retries throttling and does not reuse the edit's context, which that edit
	// may have spent.
	noticeCtx, noticeCancel := context.WithTimeout(a.ctx, finalSendTimeout)
	defer noticeCancel()
	if placeholderID != 0 {
		if err := worker.api.editMessageRetry(ctx, chatID, placeholderID, text); err != nil {
			_ = worker.api.deleteMessage(noticeCtx, chatID, placeholderID)
			if _, sendErr := worker.api.sendMessageRetry(noticeCtx, chatID, threadID, text); sendErr != nil {
				reportError("send failure message: " + sendErr.Error())
			}
		}
	} else if _, err := worker.api.sendMessageRetry(noticeCtx, chatID, threadID, text); err != nil {
		reportError("send failure message: " + err.Error())
	}
	if statusID != 0 {
		_ = worker.api.deleteMessage(noticeCtx, chatID, statusID)
	}
	a.scheduleOutputCleanup(ev.RunID, st)
}

func telegramFailureText(ev lifecycleEvent) string {
	detail := cmp.Or(strings.TrimSpace(ev.ErrorMessage), strings.TrimSpace(ev.Reason))
	if detail == "" {
		detail = "模型请求失败，请稍后重试。"
	}
	return "⚠️ 模型请求失败：\n" + detail
}

func (a *telegramApp) output(runID string) *outputState {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	return a.outputs[runID]
}

func (a *telegramApp) scheduleOutputCleanup(runID string, st *outputState) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if a.outputs[runID] != st {
		return
	}
	if st.cleanup != nil {
		st.cleanup.Stop()
	}
	st.cleanup = time.AfterFunc(outputRetention, func() {
		a.outputMu.Lock()
		if a.outputs[runID] == st {
			delete(a.outputs, runID)
		}
		a.outputMu.Unlock()
	})
}

func (a *telegramApp) flushOutput(runID string, final bool) {
	a.outputMu.Lock()
	st := a.outputs[runID]
	if st == nil || st.failed || (!final && (st.final || st.settled)) {
		a.outputMu.Unlock()
		return
	}
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	a.outputMu.Unlock()

	// Failure handling and preview flushing share one send lock. Without the
	// second state check, a timer that already captured partial text could send
	// "pong" after failOutput had replaced the placeholder with an error.
	st.sendMu.Lock()
	defer st.sendMu.Unlock()
	a.outputMu.Lock()
	if current := a.outputs[runID]; current != st || st.failed || (!final && (st.final || st.settled)) {
		a.outputMu.Unlock()
		return
	}
	text := strings.TrimSpace(st.text)
	worker := st.worker
	chatID, threadID := st.chatID, st.threadID
	private, draftID, draftOK, placeholderID, statusID := st.private, st.draftID, st.draftOK, st.placeholderID, st.statusID
	// Every write to the chat is paced, and a preview that arrives inside the
	// pacing window is skipped; the final write is never skipped, so the answer
	// reaches Telegram even when it completes inside that window.
	paced := st.reserveWrite(time.Now())
	a.outputMu.Unlock()
	if text == "" {
		if final {
			a.scheduleOutputCleanup(runID, st)
		}
		return
	}
	if !final {
		if !paced {
			return
		}
		ctx, cancel := context.WithTimeout(a.ctx, previewSendTimeout)
		defer cancel()
		part := splitTelegram(text)[0]
		if private && draftOK {
			if draftID == 0 {
				draftID = worker.draftSeq.Add(1)
			}
			if err := worker.api.sendMessageDraft(ctx, chatID, threadID, draftID, part); err == nil {
				a.outputMu.Lock()
				if current := a.outputs[runID]; current == st {
					current.draftID, current.draftOK = draftID, true
				}
				a.outputMu.Unlock()
				return
			}
			draftOK = false
			a.outputMu.Lock()
			if current := a.outputs[runID]; current == st {
				current.draftOK = false
			}
			a.outputMu.Unlock()
		}
		if placeholderID == 0 {
			if sent, err := worker.api.sendMessage(ctx, chatID, threadID, "…"); err == nil {
				placeholderID = sent.MessageID
				a.outputMu.Lock()
				if current := a.outputs[runID]; current == st {
					current.placeholderID = placeholderID
				}
				a.outputMu.Unlock()
			}
		}
		if placeholderID != 0 {
			// A rejected preview is dropped on purpose: the next tick or the final
			// edit carries the newer text, and retrying would deepen a throttle
			// that is already rejecting this chat's writes.
			_ = worker.api.editMessage(ctx, chatID, placeholderID, part)
		}
		return
	}
	a.flushFinal(worker, chatID, threadID, placeholderID, statusID, text)
	a.scheduleOutputCleanup(runID, st)
}

// flushFinal writes the completed answer. Every part gets its own budget so a
// slow or throttled earlier part cannot spend the deadline the remaining parts
// need, and a write that fails is reported: an answer that does not reach
// Telegram must not disappear without a trace.
func (a *telegramApp) flushFinal(w *telegramWorker, chatID, threadID, placeholderID, statusID int64, text string) {
	for i, part := range splitTelegram(text) {
		ctx, cancel := context.WithTimeout(a.ctx, finalSendTimeout)
		if i == 0 && placeholderID != 0 {
			a.writeFinalPlaceholder(ctx, w, chatID, threadID, placeholderID, part)
		} else if _, err := w.api.sendMessageRetry(ctx, chatID, threadID, part); err != nil {
			reportError("send final message: " + err.Error())
		}
		cancel()
	}
	if statusID != 0 {
		ctx, cancel := context.WithTimeout(a.ctx, finalSendTimeout)
		_ = w.api.deleteMessage(ctx, chatID, statusID)
		cancel()
	}
}

// writeFinalPlaceholder writes the final text into the group's placeholder
// message. The placeholder *is* the reply, so a failed edit cannot be dropped:
// throttled edits are retried, and a still-failing edit falls back to a fresh
// message and removes the stub. The fallback gets its own context because the
// failed edit may have spent the caller's deadline.
func (a *telegramApp) writeFinalPlaceholder(ctx context.Context, w *telegramWorker, chatID, threadID, placeholderID int64, part string) {
	err := w.api.editMessageRetry(ctx, chatID, placeholderID, part)
	if err == nil {
		return
	}
	// The placeholder may also be gone (removed by hand, or a rejected edit that
	// Telegram never applied), so a fresh message is the only way to show the
	// answer. The stub is deleted only after that send succeeded: deleting first
	// would destroy a partial answer when the fallback also fails.
	reportError("edit final message: " + err.Error())
	sendCtx, cancel := context.WithTimeout(a.ctx, finalSendTimeout)
	defer cancel()
	if _, sendErr := w.api.sendMessageRetry(sendCtx, chatID, threadID, part); sendErr != nil {
		reportError("send final message: " + sendErr.Error())
		return
	}
	if err := w.api.deleteMessage(sendCtx, chatID, placeholderID); err != nil {
		reportError("delete placeholder message: " + err.Error())
	}
}

func (a *telegramApp) sendToolStatus(ev lifecycleEvent, w *telegramWorker) {
	title := toolTitle(ev.ToolTitle, ev.ToolName)
	st := a.ensureOutput(ev, w)
	if st == nil {
		return
	}
	st.sendMu.Lock()
	defer st.sendMu.Unlock()
	a.outputMu.Lock()
	settled := st.failed || st.settled
	// Tool status and preview share one per-chat write budget: a run with many
	// tool calls would otherwise spend the budget the reply's placeholder edit
	// needs. A skipped status keeps the previous tool row.
	write := st.reserveWrite(time.Now())
	a.outputMu.Unlock()
	if settled || !write {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	sent, err := w.api.sendMessage(ctx, st.chatID, st.threadID, "🔧 "+title)
	cancel()
	if err != nil {
		return
	}
	a.outputMu.Lock()
	if current := a.outputs[ev.RunID]; current == st {
		if current.statusID != 0 {
			old := current.statusID
			go func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(a.ctx, 5*time.Second)
				_ = w.api.deleteMessage(cleanupCtx, st.chatID, old)
				cleanupCancel()
			}()
		}
		current.statusID = sent.MessageID
	}
	a.outputMu.Unlock()
}

func toolTitle(title, name string) string {
	if title != "" {
		return title
	}
	switch name {
	case "Read":
		return "读取文件"
	case "Write":
		return "写入文件"
	case "Edit":
		return "修改文件"
	case "Bash", "PowerShell":
		return "执行命令"
	case "Grep", "Glob":
		return "搜索文件"
	default:
		return name
	}
}

func main() {
	rpc := newStdioRPC(os.Stdin, os.Stdout)
	app := newTelegramApp(rpc)
	rpc.onRequest(app.handle)
	if err := rpc.serve(context.Background(), os.Stdin); err != nil && err != io.EOF {
		reportError("rpc server: " + err.Error())
	}
	app.close()
}
