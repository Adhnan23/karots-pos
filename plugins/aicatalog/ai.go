package aicatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	cfg Settings
	hc  *http.Client
}

func NewClient(cfg Settings) *Client {
	return &Client{cfg: cfg, hc: &http.Client{Timeout: 20 * time.Second}}
}

const systemPrompt = `You identify retail/spare-part products from a short code or name.
Reply with STRICT JSON ONLY, no prose, matching:
{"confident":bool,"best":{"name":"","category":"","specs":"","explanation":""},
"options":[{"name":"","category":"","specs":"","explanation":""}]}
Set confident=true and fill "best" only when you are sure. Otherwise set
confident=false and give up to 4 "options". "category" is a hierarchical path
from broad to specific using " > " between levels, e.g. "Bearings > Deep Groove"
or "Beverages > Soft Drinks". Prefer 2 levels (broad parent > specific child) so
similar items group together. "explanation" is one short sentence a non-expert
understands. "specs" holds key dimensions/ratings if known.`

type chatReq struct {
	Model    string    `json:"model"`
	Messages []chatMsg `json:"messages"`
}
type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatResp struct {
	Choices []struct {
		Message chatMsg `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) call(ctx context.Context, messages []chatMsg) (string, error) {
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return "", errors.New("AI API key is not set — add it in AI Catalog settings")
	}
	payload, _ := json.Marshal(chatReq{Model: c.cfg.Model, Messages: messages})
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("AI request failed (HTTP %d): %s", resp.StatusCode, apiErrMessage(body))
	}
	var cr chatResp
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("AI returned unreadable response (HTTP %d): %s", resp.StatusCode, snippet(body))
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return "", errors.New(cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", errors.New("AI returned no answer")
	}
	return cr.Choices[0].Message.Content, nil
}

// snippet returns a short, single-blob preview of a response body for errors.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// apiErrMessage pulls a human message out of either an OpenAI-style
// {"error":{"message":...}} body or a Gemini-style [{"error":{"message":...}}]
// array, falling back to a raw snippet.
func apiErrMessage(b []byte) string {
	type errShape struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var obj errShape
	if json.Unmarshal(b, &obj) == nil && obj.Error.Message != "" {
		return obj.Error.Message
	}
	var arr []errShape
	if json.Unmarshal(b, &arr) == nil && len(arr) > 0 && arr[0].Error.Message != "" {
		return arr[0].Error.Message
	}
	return snippet(b)
}

func (c *Client) Identify(ctx context.Context, query, userHint string) (IdentifyResult, error) {
	user := "Identify this item: " + query
	if strings.TrimSpace(userHint) != "" {
		user += "\nAdditional context from the shop owner: " + userHint
	}
	content, err := c.call(ctx, []chatMsg{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: user},
	})
	if err != nil {
		return IdentifyResult{}, err
	}
	return parseIdentify([]byte(content))
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.call(ctx, []chatMsg{{Role: "user", Content: "reply with the single word: ok"}})
	return err
}
