package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	stateFileName   = "state.json"
	configFileName  = "config.json"
	outputDebounce  = 250 * time.Millisecond
	settleDelay     = 500 * time.Millisecond
	outputRetention = 30 * time.Second
	secretValue     = "<configured>"
)

type telegramConfig struct {
	BotID string `json:"botId"`
	Token string `json:"token"`
	Model string `json:"model"`
}

type telegramState struct {
	Offsets  map[string]int64  `json:"offsets"`
	Sessions map[string]string `json:"sessions"`
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
}

func newTelegramApp(rpc *stdioRPC) *telegramApp {
	home := os.Getenv("KI_HOME")
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".ki")
		}
	}
	root := os.Getenv("KI_EXTENSION_ROOT")
	if root == "" {
		root = filepath.Join(home, "extensions", "telegram-bot")
	}
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
		botID: botID, accountID: "bot:" + strconv.FormatInt(botID, 10), model: strings.TrimSpace(config.Model),
	}
	a.workersMu.Lock()
	a.workers[worker.accountID] = worker
	a.workersMu.Unlock()
	go worker.run()
	return nil
}

func loadTelegramConfig(path string) (telegramConfig, error) {
	cfg := telegramConfig{}
	b, err := os.ReadFile(path) //nolint:gosec // path is the configured extension directory.
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
	state := telegramState{Offsets: map[string]int64{}, Sessions: map[string]string{}}
	b, err := os.ReadFile(path) //nolint:gosec // path is the private extension state file.
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, err
	}
	if state.Offsets == nil {
		state.Offsets = map[string]int64{}
	}
	if state.Sessions == nil {
		state.Sessions = map[string]string{}
	}
	return state, nil
}

func (a *telegramApp) persistStateLocked() error {
	if err := os.MkdirAll(filepath.Dir(a.statePath), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.statePath, append(b, '\n'), 0o600) //nolint:gosec // path is the private extension state file.
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
	if msg == nil || msg.From == nil || msg.From.IsBot {
		return nil
	}
	group := isGroup(msg.Chat)
	text := msg.Text
	entities := msg.Entities
	if text == "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}
	addressed := !group || mentionsBot(text, entities, w.me)
	if group && addressed {
		text = stripBotMention(text, entities, w.me)
	}
	// Telegram enforces Managed Bot access restrictions. The mention only
	// controls whether a group message starts a run; all received group
	// messages are retained as context for the next addressed message.
	name, args, slash := parseSlash(text)
	control := slash && (name == "new" || name == "cwd" || name == "compact" || name == "reload")
	// Ordinary Bot API updates do not expose a user-client read marker. A
	// reaction is the visible acknowledgement and is deliberately best effort.
	reactionCtx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	_ = w.api.setReaction(reactionCtx, msg.Chat.ID, msg.MessageID)
	cancel()

	key := w.externalKey(msg.Chat.ID, msg.MessageThreadID)
	sess, err := w.sessionFor(key, msg)
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
	external := w.externalMetadata(msg)
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
		w.sendText(msg.Chat.ID, msg.MessageThreadID, "⚠️ Telegram 回复模型配置无效：\n"+err.Error())
		return nil
	}
	if control {
		return w.runCommand(name, args, key, sess, msg)
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
	Size     int64  `json:"size,omitempty"`
}

func hasInput(text string, contents []inputContent) bool {
	return len(contents) > 0 || strings.TrimSpace(text) != ""
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
	b, err := w.api.download(ctx, info.FilePath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, b, 0o600); err != nil { //nolint:gosec // target is inside the session cwd.
		return "", err
	}
	return target, nil
}

func (w *telegramWorker) externalKey(chatID, threadID int64) string {
	return "telegram:" + w.accountID + ":" + strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(threadID, 10)
}

func (w *telegramWorker) externalMetadata(msg *message) map[string]string {
	return map[string]string{
		"source":      "telegram",
		"connector":   "telegram-bot",
		"accountId":   w.accountID,
		"externalKey": w.externalKey(msg.Chat.ID, msg.MessageThreadID),
		"chatId":      strconv.FormatInt(msg.Chat.ID, 10),
		"threadId":    strconv.FormatInt(msg.MessageThreadID, 10),
		"chatType":    msg.Chat.Type,
		"userId":      userID(msg.From),
		"messageId":   strconv.FormatInt(msg.MessageID, 10),
	}
}

func (w *telegramWorker) sessionFor(key string, msg *message) (sessionSnapshot, error) {
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
	cwd := w.workspaceCWD(msg.Chat.ID, msg.MessageThreadID)
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return sessionSnapshot{}, err
	}
	metadata := map[string]any{}
	for key, value := range w.externalMetadata(msg) {
		metadata[key] = value
	}
	var created sessionCreateResult
	ctx, cancel = context.WithTimeout(w.ctx, 15*time.Second)
	params := map[string]any{
		"cwd":      cwd,
		"metadata": metadata,
	}
	if w.model != "" {
		params["model"] = w.model
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
	if w.model == "" || sess.ID == "" || w.model == sess.Model || w.model == sess.Provider+"/"+sess.Model {
		return nil
	}
	ctx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
	defer cancel()
	return w.app.rpc.call(ctx, "session.patch", map[string]any{"sessionId": sess.ID, "model": w.model}, nil)
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

func (w *telegramWorker) runCommand(name, args, key string, sess sessionSnapshot, msg *message) error {
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	switch name {
	case "new":
		var result sessionCreateResult
		if err := w.app.rpc.call(ctx, "session.new", map[string]any{"sessionId": sess.ID}, &result); err != nil {
			w.sendText(msg.Chat.ID, msg.MessageThreadID, "新会话创建失败："+err.Error())
			return nil
		}
		w.updateMapping(key, result.SessionID)
		w.sendText(msg.Chat.ID, msg.MessageThreadID, "已开启新会话。")
	case "cwd":
		if strings.TrimSpace(args) == "" {
			w.sendText(msg.Chat.ID, msg.MessageThreadID, "用法：/cwd <path>")
			return nil
		}
		path := strings.TrimSpace(args)
		if !filepath.IsAbs(path) {
			path = filepath.Join(sess.CWD, path)
		}
		path, err := filepath.Abs(path)
		if err != nil {
			w.sendText(msg.Chat.ID, msg.MessageThreadID, "工作目录无效："+err.Error())
			return nil
		}
		var result sessionCreateResult
		if err := w.app.rpc.call(ctx, "session.new", map[string]any{"sessionId": sess.ID, "cwd": path}, &result); err != nil {
			w.sendText(msg.Chat.ID, msg.MessageThreadID, "切换工作目录失败："+err.Error())
			return nil
		}
		w.updateMapping(key, result.SessionID)
		w.sendText(msg.Chat.ID, msg.MessageThreadID, "工作目录已切换到：\n"+result.CWD)
	case "compact":
		if err := w.app.rpc.call(ctx, "session.compact", map[string]any{"sessionId": sess.ID}, nil); err != nil {
			w.sendText(msg.Chat.ID, msg.MessageThreadID, "压缩失败："+err.Error())
			return nil
		}
		w.sendText(msg.Chat.ID, msg.MessageThreadID, "会话已压缩。")
	case "reload":
		if err := w.app.rpc.call(ctx, "session.reload", map[string]any{"sessionId": sess.ID}, nil); err != nil {
			w.sendText(msg.Chat.ID, msg.MessageThreadID, "重载失败："+err.Error())
			return nil
		}
		w.sendText(msg.Chat.ID, msg.MessageThreadID, "会话扩展资源已重载。")
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
		external: cloneStringMap(ev.External), draftOK: true,
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
			st.timer = time.AfterFunc(outputDebounce, func() { a.flushOutput(ev.RunID, false) })
		}
	}
	a.outputMu.Unlock()
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
	text := override
	if text == "" {
		text = telegramFailureText(ev)
	}
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
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if private && draftOK && draftID != 0 {
		// Replace a private draft before sending the durable error message.
		// The final send also closes the draft on Telegram clients that support
		// sendMessageDraft.
		_ = worker.api.sendMessageDraft(ctx, chatID, threadID, draftID, text)
	}
	if placeholderID != 0 {
		if err := worker.api.editMessage(ctx, chatID, placeholderID, text); err != nil {
			_ = worker.api.deleteMessage(ctx, chatID, placeholderID)
			if _, sendErr := worker.api.sendMessage(ctx, chatID, threadID, text); sendErr != nil {
				reportError("send failure message: " + sendErr.Error())
			}
		}
	} else if _, err := worker.api.sendMessage(ctx, chatID, threadID, text); err != nil {
		reportError("send failure message: " + err.Error())
	}
	if statusID != 0 {
		_ = worker.api.deleteMessage(ctx, chatID, statusID)
	}
	a.scheduleOutputCleanup(ev.RunID, st)
}

func telegramFailureText(ev lifecycleEvent) string {
	detail := strings.TrimSpace(ev.ErrorMessage)
	if detail == "" {
		detail = strings.TrimSpace(ev.Reason)
	}
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
	a.outputMu.Unlock()
	if text == "" {
		if final {
			a.scheduleOutputCleanup(runID, st)
		}
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if !final {
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
			_ = worker.api.editMessage(ctx, chatID, placeholderID, part)
		}
		return
	}
	parts := splitTelegram(text)
	for i, part := range parts {
		if i == 0 && placeholderID != 0 {
			if err := worker.api.editMessage(ctx, chatID, placeholderID, part); err != nil {
				_, _ = worker.api.sendMessage(ctx, chatID, threadID, part)
			}
			continue
		}
		if _, err := worker.api.sendMessage(ctx, chatID, threadID, part); err != nil {
			reportError("send final message: " + err.Error())
		}
	}
	if statusID != 0 {
		_ = worker.api.deleteMessage(ctx, chatID, statusID)
	}
	a.scheduleOutputCleanup(runID, st)
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
	settled := st == nil || st.failed || st.settled
	a.outputMu.Unlock()
	if settled {
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
	case "Edit", "apply_patch":
		return "修改文件"
	case "Bash", "PowerShell":
		return "执行命令"
	case "Grep", "Glob":
		return "搜索文件"
	default:
		return name
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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
