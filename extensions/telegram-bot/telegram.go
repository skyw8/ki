package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const telegramMessageLimit = 4096

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
	base := os.Getenv("KI_TELEGRAM_API_BASE")
	if base == "" {
		base = "https://api.telegram.org"
	}
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

func (a *botAPI) getMe(ctx context.Context) (user, error) {
	var out user
	err := a.call(ctx, "getMe", map[string]any{}, &out)
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
	callCtx, cancel := context.WithTimeout(ctx, 50*timeSecond)
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

func (a *botAPI) sendMessage(ctx context.Context, chatID, threadID int64, text string) (message, error) {
	params := map[string]any{"chat_id": chatID, "text": text}
	if threadID != 0 {
		params["message_thread_id"] = threadID
	}
	var out message
	err := a.call(ctx, "sendMessage", params, &out)
	return out, err
}

func (a *botAPI) sendMessageDraft(ctx context.Context, chatID, threadID, draftID int64, text string) error {
	params := map[string]any{"chat_id": chatID, "draft_id": draftID, "text": text}
	if threadID != 0 {
		params["message_thread_id"] = threadID
	}
	return a.call(ctx, "sendMessageDraft", params, nil)
}

func (a *botAPI) editMessage(ctx context.Context, chatID, messageID int64, text string) error {
	return a.call(ctx, "editMessageText", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}, nil)
}

func (a *botAPI) deleteMessage(ctx context.Context, chatID, messageID int64) error {
	return a.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

func (a *botAPI) sendChatAction(ctx context.Context, chatID, threadID int64) error {
	params := map[string]any{"chat_id": chatID, "action": "typing"}
	if threadID != 0 {
		params["message_thread_id"] = threadID
	}
	return a.call(ctx, "sendChatAction", params, nil)
}

func (a *botAPI) getFile(ctx context.Context, fileID string) (fileInfo, error) {
	var out fileInfo
	err := a.call(ctx, "getFile", map[string]any{"file_id": fileID}, &out)
	return out, err
}

func (a *botAPI) download(ctx context.Context, filePath string) ([]byte, error) {
	url := a.base + "/file/bot" + a.token + "/" + strings.TrimLeft(filePath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram file download returned %s", res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 50<<20))
}

// Telegram's long-poll timeout is expressed separately to avoid importing a
// second duration helper into the sidecar's small API client.
const timeSecond = 1_000_000_000

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
}

type chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

type message struct {
	MessageID       int64       `json:"message_id"`
	MessageThreadID int64       `json:"message_thread_id,omitempty"`
	From            *user       `json:"from,omitempty"`
	Chat            chat        `json:"chat"`
	Text            string      `json:"text,omitempty"`
	Caption         string      `json:"caption,omitempty"`
	Entities        []entity    `json:"entities,omitempty"`
	CaptionEntities []entity    `json:"caption_entities,omitempty"`
	Photo           []photoSize `json:"photo,omitempty"`
	Document        *document   `json:"document,omitempty"`
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
	FileSize int64  `json:"file_size,omitempty"`
}

type document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MIMEType string `json:"mime_type"`
	FileSize int64  `json:"file_size,omitempty"`
}

type fileInfo struct {
	FilePath string `json:"file_path"`
}

func isGroup(c chat) bool { return c.Type == "group" || c.Type == "supergroup" }

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
		n := telegramMessageLimit
		if len(runes) < n {
			n = len(runes)
		}
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
