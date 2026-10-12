package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"budget-tracking/backend/internal/assistant"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/ledger"
)

const (
	chatTimeout    = 60 * time.Second
	maxTitleLength = 80
)

type chatView struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type chatMessageView struct {
	ID        int64           `json:"id"`
	Question  string          `json:"question"`
	Answer    json.RawMessage `json:"answer"`
	CreatedAt string          `json:"createdAt"`
}

type chatDetail struct {
	chatView
	Messages []chatMessageView `json:"messages"`
}

func newChatView(chat dbgen.Chat) chatView {
	return chatView{ID: chat.ID, Title: chat.Title, CreatedAt: chat.CreatedAt, UpdatedAt: chat.UpdatedAt}
}

func newChatMessageView(message dbgen.ChatMessage) chatMessageView {
	return chatMessageView{ID: message.ID, Question: message.Question, Answer: json.RawMessage(message.AnswerJson), CreatedAt: message.CreatedAt}
}

func (a *api) listChats(w http.ResponseWriter, r *http.Request, _ *session) {
	rows, err := a.store.Queries.ListChats(r.Context())
	if err != nil {
		internalError(w, "list chats")
		return
	}
	views := make([]chatView, 0, len(rows))
	for _, row := range rows {
		views = append(views, newChatView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": views, "examples": assistant.Examples()})
}

func (a *api) getChat(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Chat")
	if !ok {
		return
	}
	chat, err := a.store.Queries.GetChat(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if err != nil {
		internalError(w, "get chat")
		return
	}
	messages, err := a.store.Queries.ListChatMessages(r.Context(), id)
	if err != nil {
		internalError(w, "list chat messages")
		return
	}
	detail := chatDetail{chatView: newChatView(chat), Messages: make([]chatMessageView, 0, len(messages))}
	for _, message := range messages {
		detail.Messages = append(detail.Messages, newChatMessageView(message))
	}
	writeJSON(w, http.StatusOK, detail)
}

type questionInput struct {
	Question string `json:"question"`
}

// createChat starts a chat with its first question.
func (a *api) createChat(w http.ResponseWriter, r *http.Request, current *session) {
	a.ask(w, r, current, 0)
}

func (a *api) askInChat(w http.ResponseWriter, r *http.Request, current *session) {
	id, ok := pathID(w, r, "Chat")
	if !ok {
		return
	}
	a.ask(w, r, current, id)
}

// ask answers a question and stores it. chatID 0 starts a new chat. Jev
// is asked before anything is written, so a failed answer leaves no trace.
func (a *api) ask(w http.ResponseWriter, r *http.Request, current *session, chatID int64) {
	var input questionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	question, valid := assistant.CleanQuestion(input.Question)
	if !valid {
		writeFieldErrors(w, map[string]string{"question": "Ask a question of up to 500 characters"})
		return
	}
	var previous *assistant.Turn
	if chatID != 0 {
		if _, err := a.store.Queries.GetChat(r.Context(), chatID); errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Chat not found")
			return
		} else if err != nil {
			internalError(w, "get chat")
			return
		}
		latest, err := a.store.Queries.LatestChatMessage(r.Context(), chatID)
		switch {
		case err == nil:
			turn := assistant.Turn{Question: latest.Question}
			if json.Unmarshal([]byte(latest.InterpretationJson), &turn.Interpretation) == nil {
				previous = &turn
			}
		case !errors.Is(err, sql.ErrNoRows):
			internalError(w, "load previous question")
			return
		}
	}
	apiKey, err := a.typeSafeKey(r.Context())
	if err != nil {
		internalError(w, "load typesafe integration")
		return
	}
	if apiKey == "" {
		writeError(w, http.StatusConflict, "Connect TypeSafe in Settings to ask questions")
		return
	}
	extendDeadlines(w, 0, chatTimeout+10*time.Second)
	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	ask := func(ctx context.Context, state any, questions map[string]integrations.Question) (map[string]integrations.Answer, error) {
		return a.integrations.Ask(ctx, apiKey, state, questions)
	}
	answer, interpretation, err := assistant.Respond(ctx, a.store.DB, ask, question, previous, a.now().In(a.location(current)))
	if err != nil {
		var typeSafeError *integrations.SuggestError
		if errors.As(err, &typeSafeError) || errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("chat question failed", "error", err)
			writeError(w, http.StatusBadGateway, "TypeSafe could not answer the question. Check the TypeSafe integration or try again later.")
			return
		}
		internalError(w, "answer question")
		return
	}
	answerJSON, err := json.Marshal(answer)
	if err != nil {
		internalError(w, "encode answer")
		return
	}
	interpretationJSON, err := json.Marshal(interpretation)
	if err != nil {
		internalError(w, "encode interpretation")
		return
	}
	tx, err := a.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, "begin chat")
		return
	}
	defer tx.Rollback()
	queries := a.store.Queries.WithTx(tx)
	status := http.StatusOK
	if chatID == 0 {
		chat, err := queries.CreateChat(r.Context(), chatTitle(question))
		if err != nil {
			internalError(w, "create chat")
			return
		}
		chatID, status = chat.ID, http.StatusCreated
	}
	message, err := queries.CreateChatMessage(r.Context(), dbgen.CreateChatMessageParams{
		ChatID: chatID, Question: question, AnswerJson: string(answerJSON), InterpretationJson: string(interpretationJSON),
	})
	if err != nil {
		// The chat may have been deleted while Jev was answering.
		if _, missing := queries.GetChat(r.Context(), chatID); errors.Is(missing, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Chat not found")
			return
		}
		internalError(w, "save chat message")
		return
	}
	chat, err := queries.TouchChat(r.Context(), chatID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if err != nil {
		internalError(w, "update chat")
		return
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit chat")
		return
	}
	writeJSON(w, status, map[string]any{"chat": newChatView(chat), "message": newChatMessageView(message)})
}

// chatTitle is the first question, cut at a word near maxTitleLength.
func chatTitle(question string) string {
	title := strings.Join(strings.Fields(ledger.DisplayName(question)), " ")
	if utf8.RuneCountInString(title) <= maxTitleLength {
		return title
	}
	runes := []rune(title)[:maxTitleLength]
	cut := string(runes)
	if space := strings.LastIndex(cut, " "); space > maxTitleLength/2 {
		cut = cut[:space]
	}
	return strings.TrimRight(cut, " ,.;:") + "..."
}

type renameChatInput struct {
	Title string `json:"title"`
}

func (a *api) renameChat(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Chat")
	if !ok {
		return
	}
	var input renameChatInput
	if !decodeJSON(w, r, &input) {
		return
	}
	title := strings.TrimSpace(input.Title)
	if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > maxTitleLength {
		writeFieldErrors(w, map[string]string{"title": "Enter a title of up to 80 characters"})
		return
	}
	chat, err := a.store.Queries.RenameChat(r.Context(), dbgen.RenameChatParams{Title: title, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if err != nil {
		internalError(w, "rename chat")
		return
	}
	writeJSON(w, http.StatusOK, newChatView(chat))
}

func (a *api) deleteChat(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Chat")
	if !ok {
		return
	}
	deleted, err := a.store.Queries.DeleteChat(r.Context(), id)
	if err != nil {
		internalError(w, "delete chat")
		return
	}
	if deleted == 0 {
		writeError(w, http.StatusNotFound, "Chat not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
