package theme

import (
	"context"
	"database/sql"
	"errors"

	"karots-pos/internal/apperr"

	"github.com/jmoiron/sqlx"
)

var (
	validModes     = map[string]bool{"light": true, "dark": true, "auto": true}
	validDensities = map[string]bool{"comfortable": true, "compact": true, "large_touch": true}
)

// Input is a create/update payload for a custom theme.
type Input struct {
	Name    string  `form:"name"`
	Palette string  `form:"palette"`
	Mode    string  `form:"mode"`
	Density string  `form:"density"`
	Accent  *string `form:"accent"`
}

func validateInput(in Input) error {
	if in.Name == "" {
		return apperr.Validation("theme name is required")
	}
	if _, ok := palettes[in.Palette]; !ok {
		return apperr.Validation("unknown palette")
	}
	if !validModes[in.Mode] {
		return apperr.Validation("mode must be light, dark or auto")
	}
	if !validDensities[in.Density] {
		return apperr.Validation("invalid density")
	}
	if in.Accent != nil && *in.Accent != "" && !isHex(*in.Accent) {
		return apperr.Validation("custom accent must be a hex colour like #4f46e5")
	}
	return nil
}

type Repository struct{ db *sqlx.DB }

type Service struct{ repo *Repository }

func NewService(db *sqlx.DB) *Service { return &Service{repo: &Repository{db: db}} }

func (s *Service) List(ctx context.Context) ([]Theme, error) {
	var out []Theme
	err := s.repo.db.SelectContext(ctx, &out, `SELECT id,name,palette,mode,density,accent,is_builtin FROM themes ORDER BY is_builtin DESC, name`)
	if err != nil {
		return nil, apperr.Internal("failed to load themes", err)
	}
	return out, nil
}

func (s *Service) Active(ctx context.Context) (Theme, error) {
	var t Theme
	err := s.repo.db.GetContext(ctx, &t, `
		SELECT t.id,t.name,t.palette,t.mode,t.density,t.accent,t.is_builtin
		FROM themes t JOIN settings s ON s.active_theme_id = t.id WHERE s.id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		// No active set yet — fall back to the first builtin.
		if ferr := s.repo.db.GetContext(ctx, &t,
			`SELECT id,name,palette,mode,density,accent,is_builtin FROM themes ORDER BY is_builtin DESC, id LIMIT 1`); ferr != nil {
			return Theme{}, apperr.Internal("failed to load active theme", ferr)
		}
		return t, nil
	}
	if err != nil {
		return Theme{}, apperr.Internal("failed to load active theme", err)
	}
	return t, nil
}

func (s *Service) SetActive(ctx context.Context, id int64) error {
	if _, err := s.repo.db.ExecContext(ctx, `UPDATE settings SET active_theme_id=$1 WHERE id=1`, id); err != nil {
		return apperr.Internal("failed to set active theme", err)
	}
	return s.RefreshCurrent(ctx)
}

func (s *Service) Create(ctx context.Context, in Input) (Theme, error) {
	if err := validateInput(in); err != nil {
		return Theme{}, err
	}
	var t Theme
	err := s.repo.db.GetContext(ctx, &t, `
		INSERT INTO themes (name,palette,mode,density,accent,is_builtin)
		VALUES ($1,$2,$3,$4,$5,false)
		RETURNING id,name,palette,mode,density,accent,is_builtin`,
		in.Name, in.Palette, in.Mode, in.Density, in.Accent)
	if err != nil {
		return Theme{}, apperr.Internal("failed to create theme", err)
	}
	return t, nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	var t Theme
	if err := s.repo.db.GetContext(ctx, &t,
		`SELECT id,name,palette,mode,density,accent,is_builtin FROM themes WHERE id=$1`, id); err != nil {
		return apperr.NotFound("theme not found")
	}
	if t.IsBuiltin {
		return apperr.Validation("built-in themes can't be deleted")
	}
	var activeID *int64
	_ = s.repo.db.GetContext(ctx, &activeID, `SELECT active_theme_id FROM settings WHERE id=1`)
	if activeID != nil && *activeID == id {
		return apperr.Validation("can't delete the active theme — switch first")
	}
	if _, err := s.repo.db.ExecContext(ctx, `DELETE FROM themes WHERE id=$1`, id); err != nil {
		return apperr.Internal("failed to delete theme", err)
	}
	return nil
}

// RefreshCurrent loads the active theme and updates the process CSS cache.
func (s *Service) RefreshCurrent(ctx context.Context) error {
	t, err := s.Active(ctx)
	if err != nil {
		return err
	}
	SetCurrentCSS(CSSVars(t))
	return nil
}
