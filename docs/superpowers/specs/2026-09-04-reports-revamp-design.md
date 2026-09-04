# Reports revamp — design

**Date:** 2026-09-04 · **Status:** approved, implementing · **Scope:** core only (no plugins)

Enhance the admin reports section: declutter the hub, add profit + time-of-day
insight to the Sales report, and add a dedicated Peak Hours (staffing) report.
All changes are core; plugin hooks (`PluginReportCards`, `ActivityContributor`)
stay intact.

## Decisions (from brainstorming)

- **Timezone:** reuse the existing `internal/datetime` package (`datetime.Location`,
  honors `TZ`, defaults `Asia/Colombo`, tzdata embedded). No new setting, no
  migration. Hour bucketing passes `datetime.Location.String()` into SQL as
  `AT TIME ZONE $tz`.
- **Hub cleanup:** group cards + two approved cuts (below).
- **Time-of-day:** dedicated **Peak Hours** report + an hour-window filter on the
  Sales report.
- **Time picker:** hour `<select>` dropdowns (00–23), not `<input type=time>` —
  identical across Firefox/Chrome and matches the hour-bucket granularity.

## 1. Reports hub — grouped + decluttered

`reportHubCards()` gains a `Group` field. `ReportsHub` renders three labelled
groups instead of a flat alphabetical grid:

- **Sales & Customers:** Sales, Peak Hours (new), Top Products, Product Sales,
  Sales by Cashier, Returns/Refunds, Customer Dues
- **Money & Profit:** Finance/P&L, Tender/Payments, Tax Summary, Profit by
  Category, Expenses, Cash Register
- **Inventory & Suppliers:** Inventory Valuation, Low Stock, Batches/Expiry,
  Purchases, Supplier Dues, Recipe Variance, Service Profit, Losses & Recovery

Plugin-contributed cards land in a **More** group (hook preserved).

### Approved cuts (reversible: fold view + drop card + redirect old URL)

1. **Fold Daily Sales Trend** → a trend strip inside the enhanced Sales report;
   `/admin/reports/sales-trend` 302→ `/admin/reports/sales`.
2. **Merge Damage + Warranty** → one **Losses & Recovery** page; old
   `/admin/damage` and `/admin/reports/warranty` 302→ `/admin/reports/losses`.

## 2. Sales report enhancements

- **Summary cards:** count · gross · discount · net · profit + margin% · avg
  basket · items/sale · busiest hour.
- **Profit per receipt:** COGS · Profit · Margin% columns.
- **Compare vs previous period:** re-run the summary for the prior equal-length
  range; ▲▼ deltas on the cards.
- **Drill-in:** click a row → HTMX-load its line items inline.
- **Hour-window filter:** From/To hour selects, shop-local tz. CSV carries it.
- **Trend strip:** the folded day-by-day revenue/profit.

### Profit definition (mirrors Finance P&L, per sale)

Per sale, over its `sale_items` joined to `products`:
- `pt_face`  = Σ subtotal WHERE pass_through
- `ret_val`  = Σ (subtotal/qty)·returned_qty WHERE NOT pass_through
- `cogs`     = Σ (qty−returned_qty)·cost_price WHERE NOT pass_through
- `net_rev`  = total − pt_face − ret_val
- `profit`   = net_rev − cogs   ·   `margin%` = profit / net_rev · 100

Computed via a `LEFT JOIN LATERAL` on `sales.List`; summed in
`sales.Summarize`. New `Sale` fields (`COGS`, `Profit`, `Margin`) are populated
only by this query — safe (sqlx leaves unmatched fields zero elsewhere).

## 3. Peak Hours report (new)

`GET /admin/reports/peak-hours`. Buckets sales by day-of-week × hour-of-day in
shop-local tz:

```sql
SELECT EXTRACT(DOW  FROM (created_at AT TIME ZONE $tz))::int AS dow,
       EXTRACT(HOUR FROM (created_at AT TIME ZONE $tz))::int AS hour,
       COUNT(*) AS count, COALESCE(SUM(total),0) AS revenue
FROM sales WHERE status<>'void' AND created_at>=$1 AND created_at<$2
GROUP BY 1,2
```

Renders a 7×24 colour-intensity heatmap (count + revenue) plus an hour-totals
row — the "when are we busy / when can staff rest" view. Range presets + CSV.

## 4. Blast radius

- `ListFilter` gains `FromHour, ToHour *int`; `List`/`Summarize` gain tz + hour
  params (appended, existing param numbers unchanged). Callers passing no hour
  fields are unaffected.
- `Sale` gains computed profit fields — additive, safe.
- Hub feed keeps `PluginReportCards()`.
- Cuts drop routes/cards but keep handlers behind redirects; no
  `ActivityContributor` change.

## Ceilings (ponytail)

- Existing day-grouped reports keep UTC day boundaries (unchanged blast radius);
  only the new hour features use local tz. Revisit if day totals look off near
  midnight.
- Hour window is inclusive both ends (`BETWEEN from AND to`).

## Tests

Pure-Go unit tests: previous-period range calc, hour-window validation, margin
calc. SQL profit + peak-hours verified live against the dev DB.
