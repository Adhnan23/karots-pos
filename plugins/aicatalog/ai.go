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
confident=false and give up to 4 "options". "category" is a slash path suitable
for a shop, e.g. "Bearings/Deep Groove". "explanation" is one short sentence a
non-expert understands. "specs" holds key dimensions/ratings if known.`

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
	var cr chatResp
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("AI returned unreadable response (HTTP %d)", resp.StatusCode)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return "", errors.New(cr.Error.Message)
	}
	if resp.StatusCode >= 400 || len(cr.Choices) == 0 {
		return "", fmt.Errorf("AI request failed (HTTP %d)", resp.StatusCode)
	}
	return cr.Choices[0].Message.Content, nil
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
