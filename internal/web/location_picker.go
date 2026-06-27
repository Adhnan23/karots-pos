package web

import (
	"context"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/money"
	adminfragments "karots-pos/templates/fragments/admin"
)

// parseLocation turns a LocationPicker value ("locker:3", "till:5", "external")
// into a cashflow.Location. Used by every cash touchpoint that routes through
// cashflow.Move.
func parseLocation(v string) (cashflow.Location, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return cashflow.Location{}, apperr.Validation("pick a cash source")
	}
	if v == "external" {
		return cashflow.External(), nil
	}
	kind, idStr, ok := strings.Cut(v, ":")
	if !ok {
		return cashflow.Location{}, apperr.Validation("invalid cash location")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return cashflow.Location{}, apperr.Validation("invalid cash location")
	}
	switch kind {
	case "locker":
		return cashflow.Locker(id), nil
	case "till":
		return cashflow.Till(id), nil
	}
	return cashflow.Location{}, apperr.Validation("invalid cash location")
}

// cashLocationChoices lists the pickable cash endpoints — active lockers (with
// their live balance) and currently-open tills — for a LocationPicker. External
// is never a pickable own-pile, so it is not included.
func (a *adminUI) cashLocationChoices(ctx context.Context) ([]adminfragments.LocationChoice, error) {
	sym := a.symbol(ctx)
	var out []adminfragments.LocationChoice

	lockerRows, err := a.s.lockers.List(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, l := range lockerRows {
		out = append(out, adminfragments.LocationChoice{
			Value: "locker:" + strconv.FormatInt(l.ID, 10),
			Label: l.Name + " (" + money.Format(sym, l.Balance) + ")",
			Group: "Lockers",
		})
	}

	tills, err := a.s.cashRegister.OpenSessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tills {
		out = append(out, adminfragments.LocationChoice{
			Value: "till:" + strconv.FormatInt(t.UserID, 10),
			Label: "Till — " + t.UserName,
			Group: "Tills",
		})
	}
	return out, nil
}
