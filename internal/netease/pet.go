package netease

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type PetChatRequest struct {
	ClientMessageID string `json:"client_message_id"`
	SessionID       string `json:"session_id"`
	Content         string `json:"content"`
	SkinName        string `json:"skin_name"`
}

type PetHistoryRequest struct {
	PageSize       int    `json:"page_size"`
	RecordIDCursor string `json:"record_id_cursor"`
}

type PetHistoryRow struct {
	ClientMessageID  string          `json:"client_message_id"`
	RecordID         string          `json:"record_id"`
	SessionID        string          `json:"session_id"`
	UserMessage      string          `json:"user_message"`
	AssistantMessage string          `json:"assistant_message"`
	Timestamp        json.RawMessage `json:"timestamp"`
}

type PetHistoryEntity struct {
	Rows           []PetHistoryRow `json:"rows"`
	RecordIDCursor string          `json:"record_id_cursor"`
}

type PetResponse struct {
	Code    *int            `json:"code"`
	Message string          `json:"message"`
	Details string          `json:"details"`
	Entity  json.RawMessage `json:"entity"`
}

func (c *Client) petRequest(ctx context.Context, path string, payload any, account *Account) (*PetResponse, error) {
	if account == nil || account.UID <= 0 || account.Token == "" {
		return nil, fmt.Errorf("game login uid/token is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// Sign the exact bytes sent on the wire. These endpoints use plaintext JSON.
	data, err := c.postJSON(ctx, c.APIBaseURL, path, string(body), account)
	if err != nil {
		return nil, err
	}
	var response PetResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode %s JSON: %w", path, err)
	}
	if response.Code == nil {
		return nil, fmt.Errorf("%s response is missing code", path)
	}
	if *response.Code != 0 {
		return &response, fmt.Errorf("%s rejected: code=%d message=%s details=%s", path, *response.Code, response.Message, response.Details)
	}
	return &response, nil
}

func (c *Client) PetChat(ctx context.Context, account *Account, request PetChatRequest) (*PetResponse, error) {
	if strings.TrimSpace(request.Content) == "" || request.ClientMessageID == "" || request.SessionID == "" {
		return nil, fmt.Errorf("content, client_message_id and session_id are required")
	}
	return c.petRequest(ctx, "/pet-agent/chat", request, account)
}

func (c *Client) PetHistory(ctx context.Context, account *Account, cursor string, pageSize int) (*PetHistoryEntity, error) {
	if pageSize < 1 || pageSize > 100 {
		return nil, fmt.Errorf("page_size must be between 1 and 100")
	}
	response, err := c.petRequest(ctx, "/pet-agent/history", PetHistoryRequest{pageSize, cursor}, account)
	if err != nil {
		return nil, err
	}
	var history PetHistoryEntity
	if len(response.Entity) == 0 || string(response.Entity) == "null" {
		return nil, fmt.Errorf("history response is missing entity")
	}
	if err := json.Unmarshal(response.Entity, &history); err != nil {
		return nil, fmt.Errorf("decode history entity: %w", err)
	}
	return &history, nil
}

// FindPetAnswer scans a bounded number of pages, matching both IDs so an old
// answer or another concurrent session can never be mistaken for this request.
func (c *Client) FindPetAnswer(ctx context.Context, account *Account, sessionID, messageID string, maxPages int) (*PetHistoryRow, error) {
	cursor := ""
	seen := map[string]bool{}
	for i := 0; i < maxPages; i++ {
		history, err := c.PetHistory(ctx, account, cursor, 20)
		if err != nil {
			return nil, err
		}
		for _, row := range history.Rows {
			if row.SessionID == sessionID && row.ClientMessageID == messageID && strings.TrimSpace(row.AssistantMessage) != "" {
				return &row, nil
			}
		}
		next := history.RecordIDCursor
		if len(history.Rows) == 0 || next == "" || next == cursor || seen[next] {
			break
		}
		seen[next] = true
		cursor = next
	}
	return nil, nil
}

func NewPetIDs(milliseconds int64) (sessionID, messageID string) {
	return strconv.FormatInt(milliseconds, 10), "msg_" + uuid.NewString()
}
