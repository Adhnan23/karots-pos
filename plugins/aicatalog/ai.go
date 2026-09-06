package aicatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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
confident=false and give up to 4 "options". "category" uses " > " between levels,
but keep it GENERAL and SHALLOW: usually ONE broad category, at most two levels.
Only add a deeper sub-category when a shop would stock MANY items of that exact
type. Never build long chains like A > B > C > D. Prefer "Bearings" (or at most
"Bearings > Ball Bearings"), NOT "Automotive > Parts > Bearings > Deep Groove".
"explanation" is one short sentence a non-expert understands. "specs" holds key
dimensions/ratings if known.
When web search is available, ALWAYS look the item up to confirm it rather than
relying on memory — codes and part numbers are easily confused, so verify before
answering. If you cannot confirm what it is, set confident=false and offer your
best options instead of guessing a single answer.
If a list of the shop's EXISTING categories is given, and one of them fits the
item, REUSE its exact path verbatim instead of inventing a new name — so similar
items land together. Only propose a new category when none of the existing ones fit.`

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
	payload, _ := json.Marshal(chatReq{Model: c.cfg.Model, Messages: messages})
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
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

// --- Gemini native endpoint with Google Search grounding ---

type gemPart struct {
	Text string `json:"text"`
}
type gemContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []gemPart `json:"parts"`
}
type gemTool struct {
	GoogleSearch *struct{} `json:"google_search,omitempty"`
}
type gemReq struct {
	SystemInstruction *gemContent  `json:"system_instruction,omitempty"`
	Contents          []gemContent `json:"contents"`
	Tools             []gemTool    `json:"tools,omitempty"`
}
type gemResp struct {
	Candidates []struct {
		Content gemContent `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// geminiBase turns the stored OpenAI-compat base (…/v1beta/openai) into the
// native base (…/v1beta) the generateContent endpoint lives on.
func (c *Client) geminiBase() string {
	b := strings.TrimRight(strings.TrimSpace(c.cfg.BaseURL), "/")
	b = strings.TrimSuffix(b, "/openai")
	if b == "" {
		b = "https://generativelanguage.googleapis.com/v1beta"
	}
	return b
}

func (c *Client) callGemini(ctx context.Context, system, user string, search bool) (string, error) {
	reqBody := gemReq{
		Contents: []gemContent{{Role: "user", Parts: []gemPart{{Text: user}}}},
	}
	if strings.TrimSpace(system) != "" {
		reqBody.SystemInstruction = &gemContent{Parts: []gemPart{{Text: system}}}
	}
	if search {
		reqBody.Tools = []gemTool{{GoogleSearch: &struct{}{}}}
	}
	payload, _ := json.Marshal(reqBody)
	endpoint := c.geminiBase() + "/models/" + c.cfg.Model + ":generateContent?key=" + url.QueryEscape(c.cfg.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("AI request failed (HTTP %d): %s", resp.StatusCode, apiErrMessage(body))
	}
	var gr gemResp
	if err := json.Unmarshal(body, &gr); err != nil {
		return "", fmt.Errorf("AI returned unreadable response (HTTP %d): %s", resp.StatusCode, snippet(body))
	}
	if gr.Error != nil && gr.Error.Message != "" {
		return "", errors.New(gr.Error.Message)
	}
	if len(gr.Candidates) == 0 {
		return "", errors.New("AI returned no answer")
	}
	// Grounded replies can arrive as several text parts; concatenate them.
	var sb strings.Builder
	for _, p := range gr.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	if sb.Len() == 0 {
		return "", errors.New("AI returned no answer")
	}
	return sb.String(), nil
}

// identifyUserMsg builds the user turn describing the item to identify.
func identifyUserMsg(query, userHint string, existingCats []string) string {
	user := "Identify this item: " + query
	if strings.TrimSpace(userHint) != "" {
		user += "\nAdditional context from the shop owner: " + userHint
	}
	if len(existingCats) > 0 {
		user += "\n\nEXISTING shop categories (reuse a fitting one verbatim, else propose new):\n- " + strings.Join(existingCats, "\n- ")
	}
	return user
}

// IdentifyPrompt is the full copy-paste prompt for manual (bring-your-own-chatbot)
// mode: system instructions + the request in one blob the owner pastes into any
// chatbot. The reply is fed back through parseIdentify.
func IdentifyPrompt(query, userHint string, existingCats []string) string {
	return systemPrompt + "\n\n" + identifyUserMsg(query, userHint, existingCats)
}

func (c *Client) Identify(ctx context.Context, query, userHint string, existingCats []string) (IdentifyResult, error) {
	// Gemini gets live Google Search grounding; other providers answer from
	// built-in knowledge over the OpenAI-compatible endpoint.
	content, err := c.complete(ctx, systemPrompt, identifyUserMsg(query, userHint, existingCats), c.isGemini())
	if err != nil {
		return IdentifyResult{}, err
	}
	return parseIdentify([]byte(content))
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.complete(ctx, "", "reply with the single word: ok", false)
	return err
}

const optimizeSystemPrompt = `You reorganise a shop's product category tree. You are given CATEGORIES
(id|name|parentId|productCount; parentId "-" means top level) and PRODUCTS (id|name|categoryId).
Propose ONLY clearly beneficial, conservative changes. Reply STRICT JSON ONLY:
{"renames":[{"id":0,"to":"","reason":""}],
 "reparents":[{"id":0,"parent_id":0,"reason":""}],
 "merges":[{"from":0,"into":0,"reason":""}],
 "moves":[{"product_id":0,"category_id":0,"reason":""}]}
Rules:
- Keep the tree GENERAL and SHALLOW. FLATTEN over-specific categories that hold few products (say under ~5) into their broader parent or a sibling — via merge, or reparent to a broader category. Only keep a deep/very specific category when it holds MANY products. Avoid long chains like A > B > C > D.
- merges: fold duplicate / near-duplicate (or tiny over-specific) categories together; "into" is the id you keep (broader/better/plural name), "from" is removed.
- reparents: group related loose top-level categories under a broad parent (parent_id = an existing category id; use 0 to make top level).
- renames: fix casing/spelling/singular-plural for consistency.
- moves: move an obviously mis-filed product to a better EXISTING category id.
Only reference ids that appear in the lists; parent_id and category_id must be existing category ids. Use empty arrays when nothing needs changing. No prose.`

// optimizeUserMsg serialises the category tree + product sample the model reasons over.
func optimizeUserMsg(cats []CatRow, prods []ProdRow) string {
	var b strings.Builder
	b.WriteString("CATEGORIES (id|name|parentId|productCount):\n")
	for _, c := range cats {
		parent := "-"
		if c.ParentID != nil {
			parent = fmt.Sprintf("%d", *c.ParentID)
		}
		fmt.Fprintf(&b, "%d|%s|%s|%d\n", c.ID, c.Name, parent, c.Count)
	}
	b.WriteString("\nPRODUCTS (id|name|categoryId):\n")
	for _, p := range prods {
		fmt.Fprintf(&b, "%d|%s|%d\n", p.ID, p.Name, p.CategoryID)
	}
	return b.String()
}

// OptimizePrompt is the full copy-paste prompt for manual mode; the pasted reply
// is fed back through parseOptimizePlan.
func OptimizePrompt(cats []CatRow, prods []ProdRow) string {
	return optimizeSystemPrompt + "\n\n" + optimizeUserMsg(cats, prods)
}

// Optimize asks the model for a category-tidy plan over the given tree/products.
func (c *Client) Optimize(ctx context.Context, cats []CatRow, prods []ProdRow) (OptimizePlan, error) {
	content, err := c.complete(ctx, optimizeSystemPrompt, optimizeUserMsg(cats, prods), false)
	if err != nil {
		return OptimizePlan{}, err
	}
	return parseOptimizePlan([]byte(content))
}

func (c *Client) isGemini() bool { return strings.EqualFold(c.cfg.Provider, "gemini") }

// isLocal reports a local OpenAI-compatible server (Ollama, LM Studio, llama.cpp),
// which needs no API key.
func (c *Client) isLocal() bool {
	b := strings.ToLower(c.cfg.BaseURL)
	return strings.Contains(b, "localhost") || strings.Contains(b, "127.0.0.1") || strings.Contains(b, "0.0.0.0")
}

// ListModels fetches the provider's available models via the OpenAI-compatible
// GET {base}/models endpoint (works for Groq, OpenAI, OpenRouter, Gemini's compat
// path, and local servers), returning their ids sorted.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if strings.TrimSpace(c.cfg.APIKey) == "" && !c.isLocal() {
		return nil, errors.New("add the API key first, then load models")
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, apiErrMessage(body))
	}
	var mr struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, fmt.Errorf("unreadable model list: %s", snippet(body))
	}
	out := make([]string, 0, len(mr.Data))
	for _, m := range mr.Data {
		if strings.TrimSpace(m.ID) != "" {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// complete dispatches one prompt to the right backend and returns the model's
// text. search=true asks Gemini to use Google Search grounding (ignored by the
// OpenAI-compatible path, which has no built-in web search).
func (c *Client) complete(ctx context.Context, system, user string, search bool) (string, error) {
	if strings.TrimSpace(c.cfg.APIKey) == "" && !c.isLocal() {
		return "", errors.New("AI API key is not set — add it in AI Catalog settings (or use a local provider)")
	}
	if c.isGemini() {
		return c.callGemini(ctx, system, user, search)
	}
	msgs := make([]chatMsg, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, chatMsg{Role: "system", Content: system})
	}
	msgs = append(msgs, chatMsg{Role: "user", Content: user})
	return c.call(ctx, msgs)
}
