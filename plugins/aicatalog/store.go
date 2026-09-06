package aicatalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

type Store struct{ db *sqlx.DB }

func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }

type Settings struct {
	Provider      string          `db:"provider"`
	BaseURL       string          `db:"base_url"`
	Model         string          `db:"model"`
	APIKey        string          `db:"api_key"`
	DefaultMarkup decimal.Decimal `db:"default_markup"`
	ManualMode    bool            `db:"manual_mode"`
}

type Candidate struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Specs       string `json:"specs"`
	Explanation string `json:"explanation"`
}

type IdentifyResult struct {
	Confident bool        `json:"confident"`
	Best      Candidate   `json:"best"`
	Options   []Candidate `json:"options"`
}

type LearnedItem struct {
	ProductID         int64  `db:"product_id"`
	ResolvedName      string `db:"resolved_name"`
	SuggestedCategory string `db:"suggested_category"`
	Specs             string `db:"specs"`
	UserExplanation   string `db:"user_explanation"`
	Source            string `db:"source"`
	RawQuery          string `db:"raw_query"`
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var out Settings
	err := s.db.GetContext(ctx, &out,
		`SELECT provider, base_url, model, api_key, default_markup, manual_mode FROM aicatalog_settings WHERE id = 1`)
	return out, err
}

func (s *Store) SaveSettings(ctx context.Context, in Settings) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE aicatalog_settings SET provider=$1, base_url=$2, model=$3, api_key=$4, default_markup=$5, manual_mode=$6 WHERE id = 1`,
		in.Provider, in.BaseURL, in.Model, in.APIKey, in.DefaultMarkup, in.ManualMode)
	return err
}

func (s *Store) LearnItem(ctx context.Context, it LearnedItem) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO aicatalog_items
		  (product_id, resolved_name, suggested_category, specs, user_explanation, source, raw_query, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now())
		ON CONFLICT (product_id) DO UPDATE SET
		  resolved_name=$2, suggested_category=$3, specs=$4,
		  user_explanation=$5, source=$6, raw_query=$7, updated_at=now()`,
		it.ProductID, it.ResolvedName, it.SuggestedCategory, it.Specs,
		it.UserExplanation, it.Source, it.RawQuery)
	return err
}

func (s *Store) GetItem(ctx context.Context, productID int64) (*LearnedItem, error) {
	var out LearnedItem
	err := s.db.GetContext(ctx, &out, `
		SELECT product_id, resolved_name, suggested_category, specs,
		       user_explanation, source, raw_query
		FROM aicatalog_items WHERE product_id = $1`, productID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// costFromMarkup derives cost from a selling price and a markup multiple:
// cost = round(selling / markup) to a whole number. markup must be > 1.
func costFromMarkup(selling, markup decimal.Decimal) (decimal.Decimal, error) {
	if markup.LessThanOrEqual(decimal.NewFromInt(1)) {
		return decimal.Zero, errors.New("markup must be greater than 1")
	}
	return selling.Div(markup).Round(0), nil
}

// parseIdentify tolerantly parses the model's reply into an IdentifyResult:
// it strips a leading ```json fence / trailing ``` and any prose around the
// outermost JSON object before unmarshalling.
func parseIdentify(raw []byte) (IdentifyResult, error) {
	var out IdentifyResult
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j >= i {
			s = s[i : j+1]
		}
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	if err := dec.Decode(&out); err != nil {
		return IdentifyResult{}, err
	}
	return out, nil
}
