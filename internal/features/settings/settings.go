// Package settings manages the single-row shop configuration (id is always 1).
package settings

import (
	"context"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	"karots-pos/internal/db"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

type Settings struct {
	ID              int       `db:"id"                json:"id"`
	ShopName        string    `db:"shop_name"         json:"shop_name"`
	ShopNameSi      *string   `db:"shop_name_si"      json:"shop_name_si,omitempty"`
	Address         *string   `db:"address"           json:"address,omitempty"`
	Phone           *string   `db:"phone"             json:"phone,omitempty"`
	CurrencyCode    string    `db:"currency_code"     json:"currency_code"`
	CurrencySymbol  string    `db:"currency_symbol"   json:"currency_symbol"`
	ReceiptFooter   *string   `db:"receipt_footer"    json:"receipt_footer,omitempty"`
	ReceiptWidth    string    `db:"receipt_width"     json:"receipt_width"`
	LogoURL         *string   `db:"logo_url"          json:"logo_url,omitempty"`
	LogoData        string    `db:"logo_data"         json:"logo_data,omitempty"`
	TaxRegistered   bool      `db:"tax_registered"    json:"tax_registered"`
	TaxRegNo        *string   `db:"tax_reg_no"        json:"tax_reg_no,omitempty"`
	LowStockAlerts  bool      `db:"low_stock_alerts"  json:"low_stock_alerts"`
	PromptAfterSale bool      `db:"prompt_after_sale" json:"prompt_after_sale"`
	ForcePinChange        bool `db:"force_pin_change"          json:"force_pin_change"`
	AllowCashierPinChange bool `db:"allow_cashier_pin_change"  json:"allow_cashier_pin_change"`
	DefaultSaleType string    `db:"default_sale_type" json:"default_sale_type"`
	ReceiptPrinter  string    `db:"receipt_printer"   json:"receipt_printer"`
	LabelPrinter    string    `db:"label_printer"     json:"label_printer"`
	LabelWidthMM    int       `db:"label_width_mm"    json:"label_width_mm"`
	LabelHeightMM   int       `db:"label_height_mm"   json:"label_height_mm"`
	LabelGapMM      int       `db:"label_gap_mm"      json:"label_gap_mm"`
	UpdatedAt       time.Time `db:"updated_at"        json:"updated_at"`
}

type UpdateInput struct {
	ShopName        string  `json:"shop_name"         form:"shop_name"         validate:"required,min=1,max=150"`
	ShopNameSi      *string `json:"shop_name_si"      form:"shop_name_si"`
	Address         *string `json:"address"           form:"address"`
	Phone           *string `json:"phone"             form:"phone"             validate:"omitempty,max=15"`
	CurrencyCode    string  `json:"currency_code"     form:"currency_code"     validate:"required,max=10"`
	CurrencySymbol  string  `json:"currency_symbol"   form:"currency_symbol"   validate:"required,max=5"`
	LogoURL         *string `json:"logo_url"          form:"logo_url"`
	ReceiptFooter   *string `json:"receipt_footer"    form:"receipt_footer"`
	ReceiptWidth    string  `json:"receipt_width"     form:"receipt_width"     validate:"required,oneof=80 58"`
	TaxRegistered   bool    `json:"tax_registered"    form:"tax_registered"`
	TaxRegNo        *string `json:"tax_reg_no"        form:"tax_reg_no"`
	LowStockAlerts  bool    `json:"low_stock_alerts"  form:"low_stock_alerts"`
	PromptAfterSale bool    `json:"prompt_after_sale" form:"prompt_after_sale"`
	ForcePinChange        bool `json:"force_pin_change"         form:"force_pin_change"`
	AllowCashierPinChange bool `json:"allow_cashier_pin_change" form:"allow_cashier_pin_change"`
	DefaultSaleType string  `json:"default_sale_type" form:"default_sale_type" validate:"required,oneof=retail wholesale credit"`
	ReceiptPrinter  string  `json:"receipt_printer"   form:"receipt_printer"   validate:"omitempty,max=100"`
	LabelPrinter    string  `json:"label_printer"     form:"label_printer"     validate:"omitempty,max=100"`
	// *PrinterNet are the optional "network printer" text inputs (tcp://host:9100).
	// When non-empty they override the matching dropdown; merged in SettingsUpdate.
	// Not stored as separate columns.
	ReceiptPrinterNet string `json:"-" form:"receipt_printer_net" validate:"omitempty,max=100"`
	LabelPrinterNet   string `json:"-" form:"label_printer_net"   validate:"omitempty,max=100"`
	LabelWidthMM    int     `json:"label_width_mm"    form:"label_width_mm"     validate:"omitempty,min=10,max=200"`
	LabelHeightMM   int     `json:"label_height_mm"   form:"label_height_mm"    validate:"omitempty,min=10,max=200"`
	LabelGapMM      int     `json:"label_gap_mm"      form:"label_gap_mm"       validate:"omitempty,min=0,max=20"`
}

// LogoSrc returns the logo to use: the uploaded, self-contained image (works
// offline) when present, otherwise the URL. Empty means no logo.
func (s Settings) LogoSrc() string {
	if s.LogoData != "" {
		return s.LogoData
	}
	if s.LogoURL != nil {
		return *s.LogoURL
	}
	return ""
}

// nilIfEmptyStr normalizes a blank optional text input to NULL.
func nilIfEmptyStr(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

type Repository struct{ db db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{db: q} }

func (r *Repository) Get(ctx context.Context) (*Settings, error) {
	var s Settings
	err := r.db.GetContext(ctx, &s, `SELECT * FROM settings WHERE id = 1`)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) Update(ctx context.Context, in UpdateInput) error {
	// Coalesce unset label dimensions to the standard 50x25mm sticker so a blank
	// form never stores a zero size.
	w, h, gap := in.LabelWidthMM, in.LabelHeightMM, in.LabelGapMM
	if w <= 0 {
		w = 50
	}
	if h <= 0 {
		h = 25
	}
	if gap < 0 {
		gap = 0
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE settings SET
			shop_name=$1, shop_name_si=$2, address=$3, phone=$4,
			currency_code=$5, currency_symbol=$6, receipt_footer=$7,
			tax_registered=$8, tax_reg_no=$9, low_stock_alerts=$10, default_sale_type=$11,
			logo_url=$12, receipt_width=$13,
			receipt_printer=$14, label_printer=$15,
			label_width_mm=$16, label_height_mm=$17, label_gap_mm=$18,
			prompt_after_sale=$19,
			force_pin_change=$20, allow_cashier_pin_change=$21
		WHERE id = 1`,
		in.ShopName, in.ShopNameSi, in.Address, in.Phone,
		in.CurrencyCode, in.CurrencySymbol, in.ReceiptFooter,
		in.TaxRegistered, in.TaxRegNo, in.LowStockAlerts, in.DefaultSaleType,
		nilIfEmptyStr(in.LogoURL), in.ReceiptWidth,
		in.ReceiptPrinter, in.LabelPrinter, w, h, gap,
		in.PromptAfterSale,
		in.ForcePinChange, in.AllowCashierPinChange)
	return err
}

// SetLogoData stores (or clears) the uploaded logo data URI without touching the
// other settings. Used by the logo upload endpoint.
func (r *Repository) SetLogoData(ctx context.Context, dataURI string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE settings SET logo_data=$1 WHERE id = 1`, dataURI)
	return err
}

type Service struct{ repo *Repository }

func NewService(q db.Queryer) *Service { return &Service{repo: NewRepository(q)} }

// SetLogo saves an uploaded logo (data URI), or clears it when empty.
func (s *Service) SetLogo(ctx context.Context, dataURI string) error {
	if err := s.repo.SetLogoData(ctx, dataURI); err != nil {
		return apperr.Internal("failed to save logo", err)
	}
	return nil
}

func (s *Service) Get(ctx context.Context) (*Settings, error) {
	cfg, err := s.repo.Get(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to load settings", err)
	}
	return cfg, nil
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (*Settings, error) {
	if err := s.repo.Update(ctx, in); err != nil {
		return nil, apperr.Internal("failed to update settings", err)
	}
	return s.Get(ctx)
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) Get(c echo.Context) error {
	s, err := h.svc.Get(c.Request().Context())
	if err != nil {
		return err
	}
	return response.OK(c, s)
}

func (h *APIHandler) Update(c echo.Context) error {
	var in UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	s, err := h.svc.Update(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.OK(c, s)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) *Service {
	svc := NewService(db)
	api := NewAPIHandler(svc)
	g := e.Group("/api/settings", middleware.JWTAuth(cfg.JWTSecret))
	g.GET("", api.Get)
	g.PUT("", api.Update, middleware.RequireRole("admin"))
	return svc
}
