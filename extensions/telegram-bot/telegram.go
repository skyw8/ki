package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	telegramMessageLimit  = 4096
	telegramDownloadLimit = 50 << 20
)

type telegramError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *telegramError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("telegram api error %d", e.Code)
	}
	return e.Description
}

type botAPI struct {
	base   string
	token  string
	client *http.Client
}

func newBotAPI(token string) *botAPI {
	base := cmp.Or(os.Getenv("KI_TELEGRAM_API_BASE"), "https://api.telegram.org")
	return &botAPI{base: strings.TrimRight(base, "/"), token: token, client: &http.Client{}}
}

func (a *botAPI) call(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	url := a.base + "/bot" + a.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("telegram %s response: %w", method, err)
	}
	if !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &telegramError{Code: code, Description: envelope.Description, RetryAfter: envelope.Parameters.RetryAfter}
	}
	if result != nil && len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode telegram %s result: %w", method, err)
		}
	}
	return nil
}

// callRetry runs one Telegram call and retries only throttling (HTTP 429), which
// is the expected rejection for rapid per-chat writes. retry_after is honored and
// capped: the lifecycle goroutine writes every session's replies in order, so a
// long server-side delay must not stall it. A final reply must not be lost to a
// 429 that only the caller could have waited out.
func (a *botAPI) callRetry(ctx context.Context, method string, params any, result any) error {
	for attempt := 0; ; attempt++ {
		err := a.call(ctx, method, params, result)
		var apiErr *telegramError
		if !errorsAs(err, &apiErr) || apiErr.Code != http.StatusTooManyRequests || attempt >= telegramRetryAttempts {
			return err
		}
		delay := min(time.Duration(apiErr.RetryAfter)*time.Second, telegramRetryMaxDelay)
		if !waitContext(ctx, delay) {
			return err
		}
	}
}

func (a *botAPI) getMe(ctx context.Context) (user, error) {
	var out user
	err := a.call(ctx, "getMe", map[string]any{}, &out)
	return out, err
}

// getChat reads one chat's metadata. Updates may omit is_forum, and a chat title
// is only carried by the messages that change it, so a sidecar that needs either
// asks here once per chat.
func (a *botAPI) getChat(ctx context.Context, chatID int64) (chat, error) {
	var out chat
	err := a.call(ctx, "getChat", map[string]any{"chat_id": chatID}, &out)
	return out, err
}

func (a *botAPI) deleteWebhook(ctx context.Context) error {
	// Long polling and webhook delivery are mutually exclusive. Telegram should
	// not replay messages sent while this connector was offline; the connector
	// still persists its offset for retries during one active polling lifecycle.
	return a.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": true}, nil)
}

func (a *botAPI) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	var out []update
	callCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	err := a.call(callCtx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         40,
		"allowed_updates": []string{"message"},
	}, &out)
	return out, err
}

func (a *botAPI) setReaction(ctx context.Context, chatID, messageID int64) error {
	return a.call(ctx, "setMessageReaction", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction":   []map[string]string{{"type": "emoji", "emoji": "👀"}},
	}, nil)
}

// threadParams adds message_thread_id when Telegram accepts one. The General
// topic is thread 1 in API terms but is addressed by omitting the field:
// sendMessage answers thread 1 with "Bad Request: message thread not found".
func threadParams(params map[string]any, threadID int64) map[string]any {
	if threadID > 1 {
		params["message_thread_id"] = threadID
	}
	return params
}

func sendMessageParams(chatID, threadID int64, text string) map[string]any {
	return threadParams(map[string]any{"chat_id": chatID, "text": text}, threadID)
}

func (a *botAPI) sendMessage(ctx context.Context, chatID, threadID int64, text string) (message, error) {
	var out message
	err := a.call(ctx, "sendMessage", sendMessageParams(chatID, threadID, text), &out)
	return out, err
}

// sendMessageRetry is sendMessage with throttling retried: the final answer is
// the one write the connector cannot afford to lose.
func (a *botAPI) sendMessageRetry(ctx context.Context, chatID, threadID int64, text string) (message, error) {
	var out message
	err := a.callRetry(ctx, "sendMessage", sendMessageParams(chatID, threadID, text), &out)
	return out, err
}

func (a *botAPI) sendMessageDraft(ctx context.Context, chatID, threadID, draftID int64, text string) error {
	params := threadParams(map[string]any{"chat_id": chatID, "draft_id": draftID, "text": text}, threadID)
	return a.call(ctx, "sendMessageDraft", params, nil)
}

func editMessageParams(chatID, messageID int64, text string) map[string]any {
	return map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}
}

func (a *botAPI) editMessage(ctx context.Context, chatID, messageID int64, text string) error {
	return a.call(ctx, "editMessageText", editMessageParams(chatID, messageID, text), nil)
}

// editMessageRetry is editMessage with throttling retried: a group reply is
// delivered by editing its placeholder message, so a throttled edit must be
// waited out instead of being abandoned.
func (a *botAPI) editMessageRetry(ctx context.Context, chatID, messageID int64, text string) error {
	return a.callRetry(ctx, "editMessageText", editMessageParams(chatID, messageID, text), nil)
}

func (a *botAPI) deleteMessage(ctx context.Context, chatID, messageID int64) error {
	return a.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

func (a *botAPI) sendChatAction(ctx context.Context, chatID, threadID int64) error {
	params := threadParams(map[string]any{"chat_id": chatID, "action": "typing"}, threadID)
	return a.call(ctx, "sendChatAction", params, nil)
}

func (a *botAPI) getFile(ctx context.Context, fileID string) (fileInfo, error) {
	var out fileInfo
	err := a.call(ctx, "getFile", map[string]any{"file_id": fileID}, &out)
	return out, err
}

func (a *botAPI) download(ctx context.Context, filePath string, out io.Writer) error {
	url := a.base + "/file/bot" + a.token + "/" + strings.TrimLeft(filePath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("telegram file download returned %s", res.Status)
	}
	// Stream into the caller's temporary file: ReadAll retained the complete
	// attachment and its growing buffer during download. An extra byte detects
	// oversized responses instead of silently publishing a truncated attachment.
	n, err := io.Copy(out, io.LimitReader(res.Body, telegramDownloadLimit+1))
	if err != nil {
		return err
	}
	if n > telegramDownloadLimit {
		return fmt.Errorf("telegram file exceeds %d bytes", telegramDownloadLimit)
	}
	return nil
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message,omitempty"`
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	// HasTopicsEnabled reports BotFather's threaded mode for private chats: only
	// then does a private chat's message thread identify its own conversation.
	HasTopicsEnabled bool `json:"has_topics_enabled,omitempty"`
}

type chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	// IsForum is set for supergroups with topics enabled; their topics are
	// separate conversations, while a reply thread in an ordinary group is not.
	IsForum bool `json:"is_forum,omitempty"`
}

type message struct {
	MessageID       int64       `json:"message_id"`
	MessageThreadID int64       `json:"message_thread_id,omitzero"`
	From            *user       `json:"from,omitempty"`
	Chat            chat        `json:"chat"`
	Text            string      `json:"text,omitempty"`
	Caption         string      `json:"caption,omitempty"`
	Entities        []entity    `json:"entities,omitempty"`
	CaptionEntities []entity    `json:"caption_entities,omitempty"`
	Photo           []photoSize `json:"photo,omitempty"`
	Document        *document   `json:"document,omitempty"`
	ReplyTo         *message    `json:"reply_to_message,omitempty"`
	// Topic service messages are the only source of a forum topic's name; the Bot
	// API has no method to query it (getForumTopic does not exist).
	ForumTopicCreated *forumTopic `json:"forum_topic_created,omitempty"`
	ForumTopicEdited  *forumTopic `json:"forum_topic_edited,omitempty"`
}

type forumTopic struct {
	Name string `json:"name"`
}

type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *user  `json:"user,omitempty"`
}

type photoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size,omitzero"`
}

type document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MIMEType string `json:"mime_type"`
	FileSize int64  `json:"file_size,omitzero"`
}

type fileInfo struct {
	FilePath string `json:"file_path"`
}

func isGroup(c chat) bool { return c.Type == "group" || c.Type == "supergroup" }

// sessionThread is the thread id that identifies a conversation. Telegram creates
// a message thread for every reply — in forums and in ordinary groups alike
// (https://core.telegram.org/api/threads: the thread id is the id of the
// replied-to message) — but only a forum's topics are conversations of their
// own. An ordinary group therefore keeps all of its replies in one session
// instead of forking a session per replied-to message.
func sessionThread(c chat, threadID int64, botTopics, forum bool) int64 {
	// 0 is "no thread"; 1 is the General topic, which must not be addressed as a
	// thread (sendMessage rejects message_thread_id=1).
	if threadID <= 1 {
		return 0
	}
	switch {
	case isGroup(c):
		if !forum {
			return 0
		}
	case c.Type == "private":
		if !botTopics {
			return 0
		}
	default:
		return 0
	}
	return threadID
}

// repliesToBot reports whether msg answers one of the bot's own messages. In a
// group that counts as addressing the bot, exactly like a mention, so replying
// to an answer starts the next turn instead of only appending context.
func repliesToBot(msg *message, me user) bool {
	if msg == nil || msg.ReplyTo == nil || msg.ReplyTo.From == nil || me.ID == 0 {
		return false
	}
	return msg.ReplyTo.From.ID == me.ID
}

// chatDisplayName is the user-visible name of a chat, whose title lives in the
// group fields for groups and in the name fields for private chats. The username
// wins for private chats: it survives profile renames.
func chatDisplayName(c chat) string {
	if title := strings.TrimSpace(c.Title); title != "" {
		return title
	}
	if username := strings.TrimSpace(c.Username); username != "" {
		return "@" + username
	}
	return strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName))
}

// chatLabel is the workspace title for one Telegram conversation, e.g.
// group-怀仁堂, group-怀仁堂-项目讨论, private-@ki_user.
func chatLabel(c chat, threadID int64, topicName string) string {
	name := labelPart(chatDisplayName(c))
	if name == "" {
		name = strconv.FormatInt(c.ID, 10)
	}
	if c.Type == "private" {
		return "private-" + name
	}
	label := "group-" + name
	if threadID > 1 {
		if topic := labelPart(topicName); topic != "" {
			return label + "-" + topic
		}
		return label + "-thread-" + strconv.FormatInt(threadID, 10)
	}
	return label
}

// labelPart keeps a display name on one line and short enough for a sidebar.
func labelPart(name string) string {
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return ""
	}
	runes := []rune(strings.Join(fields, " "))
	if len(runes) > 40 {
		runes = runes[:40]
	}
	return string(runes)
}

func userID(u *user) string {
	if u == nil {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

func displayName(u *user) string {
	if u == nil {
		return "unknown"
	}
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if name != "" {
		return name
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return userID(u)
}

func entityText(text string, e entity) string {
	start, end, ok := utf16ByteRange(text, e.Offset, e.Length)
	if !ok {
		return ""
	}
	return text[start:end]
}

func utf16ByteRange(text string, offset, length int) (int, int, bool) {
	if offset < 0 || length < 0 {
		return 0, 0, false
	}
	start := -1
	end := -1
	units := 0
	for byteIndex, r := range text {
		if units == offset && start < 0 {
			start = byteIndex
		}
		width := len(utf16.Encode([]rune(string(r))))
		units += width
		if units == offset+length {
			end = byteIndex + utf8.RuneLen(r)
			break
		}
	}
	if start < 0 && units == offset {
		start = len(text)
	}
	if end < 0 && units == offset+length {
		end = len(text)
	}
	if start < 0 || end < start || end > len(text) {
		return 0, 0, false
	}
	return start, end, true
}

func mentionMatches(token string, me user) bool {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, "@") {
		return me.Username != "" && strings.EqualFold(strings.TrimPrefix(token, "@"), me.Username)
	}
	if strings.HasPrefix(token, "/") {
		at := strings.IndexByte(token, '@')
		return at >= 0 && me.Username != "" && strings.EqualFold(token[at+1:], me.Username)
	}
	return false
}

type byteSpan struct{ start, end int }

func mentionBoundaryBefore(text string, index int) bool {
	if index == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index])
	if unicode.IsSpace(r) || unicode.IsPunct(r) {
		return true
	}
	// Telegram usernames are ASCII. Allow adjacent CJK text while rejecting
	// email/identifier-like ASCII strings such as a@bot.
	return r >= utf8.RuneSelf
}

func mentionBoundaryAfter(text string, index int) bool {
	if index == len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[index:])
	if unicode.IsSpace(r) || unicode.IsPunct(r) {
		return true
	}
	return r >= utf8.RuneSelf
}

func fallbackMentionSpans(text string, me user) []byteSpan {
	if me.Username == "" {
		return nil
	}
	marker := "@" + me.Username
	var spans []byteSpan
	for from := 0; from < len(text); {
		relative := strings.IndexByte(text[from:], '@')
		if relative < 0 {
			break
		}
		start := from + relative
		end := start + len(marker)
		if end <= len(text) && strings.EqualFold(text[start:end], marker) && mentionBoundaryBefore(text, start) && mentionBoundaryAfter(text, end) {
			spans = append(spans, byteSpan{start: start, end: end})
		}
		from = start + 1
	}
	return spans
}

func mentionsBot(text string, entities []entity, me user) bool {
	for _, e := range entities {
		switch e.Type {
		case "text_mention":
			if e.User != nil && e.User.ID == me.ID {
				return true
			}
		case "mention", "bot_command":
			if mentionMatches(entityText(text, e), me) {
				return true
			}
		}
	}
	// Telegram normally supplies entities, but clients and forwarded/captioned
	// messages can omit them. Keep the fallback exact to this bot's username so
	// group privacy mode is not weakened into replying to every message.
	return len(fallbackMentionSpans(text, me)) > 0
}

func stripBotMention(text string, entities []entity, me user) string {
	var spans []byteSpan
	for _, e := range entities {
		start, end, ok := utf16ByteRange(text, e.Offset, e.Length)
		if !ok {
			continue
		}
		token := text[start:end]
		switch e.Type {
		case "text_mention":
			if e.User != nil && e.User.ID == me.ID {
				spans = append(spans, byteSpan{start: start, end: end})
			}
		case "mention":
			if mentionMatches(token, me) {
				spans = append(spans, byteSpan{start: start, end: end})
			}
		case "bot_command":
			if mentionMatches(token, me) {
				if at := strings.IndexByte(token, '@'); at >= 0 {
					spans = append(spans, byteSpan{start: start + at, end: end})
				}
			}
		}
	}
	if len(spans) == 0 {
		spans = fallbackMentionSpans(text, me)
	}
	for i := len(spans) - 1; i >= 0; i-- {
		text = text[:spans[i].start] + text[spans[i].end:]
	}
	return strings.TrimSpace(text)
}

func splitTelegram(text string) []string {
	runes := []rune(text)
	if len(runes) <= telegramMessageLimit {
		return []string{text}
	}
	var out []string
	for len(runes) > 0 {
		n := min(telegramMessageLimit, len(runes))
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

func safeComponent(value string) string {
	value = strings.ReplaceAll(value, ":", "-")
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 || b.String() == "." || b.String() == ".." {
		return "_"
	}
	return b.String()
}

func int64Value(value string) int64 {
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

func attachmentPath(cwd, name string) string {
	name = filepath.Base(name)
	if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
		name = "attachment.bin"
	}
	return filepath.Join(cwd, ".telegram", name)
}
