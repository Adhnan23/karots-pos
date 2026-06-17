// Global Alpine components and HTMX glue. Loaded before Alpine so the
// component factory functions exist when Alpine initializes.

// Keyboard affordance: when HTMX swaps a form into the modal container, focus
// its first field so keyboard users can type immediately (Esc closes it — see
// ModalHost). Harmless on touch.
document.addEventListener("DOMContentLoaded", function () {
  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.target && e.target.id === "modal-container") {
      const el = e.target.querySelector(
        "input:not([type=hidden]):not([type=checkbox]):not([type=radio]), select, textarea",
      );
      if (el) el.focus();
    }
  });

  // Loading bar for every HTMX request.
  document.body.addEventListener("htmx:beforeRequest", loadingStart);
  document.body.addEventListener("htmx:afterRequest", loadingStop);

  // Replace the native confirm() for hx-confirm with our styled modal.
  document.body.addEventListener("htmx:confirm", function (e) {
    if (!e.detail.question) return; // no hx-confirm on this element → proceed
    e.preventDefault();
    window.dispatchEvent(
      new CustomEvent("app-confirm", {
        detail: {
          question: e.detail.question,
          onYes: function () {
            e.detail.issueRequest(true);
          },
        },
      }),
    );
  });
});

// toastHost: renders transient notifications fired via the "show-toast" event
// (emitted by the server as an HX-Trigger header, or dispatched client-side).
function toastHost() {
  return {
    toasts: [],
    seq: 0,
    push(detail) {
      detail = detail || {};
      const id = ++this.seq;
      this.toasts.push({
        id,
        message: detail.message || "Done",
        level: detail.level || "info",
      });
      setTimeout(() => {
        this.toasts = this.toasts.filter((t) => t.id !== id);
      }, 3500);
    },
  };
}

// --- API helper for the cashier terminal ---------------------------------
async function apiFetch(method, url, body, options) {
  const opts = { method, headers: {}, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  loadingStart();
  let res;
  try {
    res = await fetch(url, opts);
  } finally {
    loadingStop();
  }
  let json = null;
  try {
    json = await res.json();
  } catch (_) {
    /* no body */
  }
  if (!res.ok) {
    const msg = (json && json.error && json.error.message) || "Request failed";
    // `silent` lets a caller handle a specific status (e.g. 404) itself without a
    // generic error toast firing first.
    if (!(options && options.silent)) {
      window.dispatchEvent(
        new CustomEvent("show-toast", { detail: { message: msg, level: "error" } })
      );
    }
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }
  return json;
}

// --- Global top loading bar -----------------------------------------------
// A thin progress bar driven by both HTMX requests and apiFetch() calls, so
// every request gives visible feedback. Counter-based so concurrent requests
// don't end the bar early.
let loadingPending = 0;
let loadingTimer = null;
function loadingBarEl() {
  return document.getElementById("app-loading-bar");
}
function loadingStart() {
  loadingPending++;
  const b = loadingBarEl();
  if (!b) return;
  b.style.opacity = "1";
  b.style.width = "35%";
  clearTimeout(loadingTimer);
  loadingTimer = setTimeout(() => {
    if (loadingPending > 0 && loadingBarEl()) loadingBarEl().style.width = "75%";
  }, 300);
}
function loadingStop() {
  loadingPending = Math.max(0, loadingPending - 1);
  const b = loadingBarEl();
  if (!b || loadingPending > 0) return;
  clearTimeout(loadingTimer);
  b.style.width = "100%";
  setTimeout(() => {
    b.style.opacity = "0";
    b.style.width = "0";
  }, 200);
}

// confirmHost: a styled replacement for the browser's confirm() dialog. Driven
// by the global htmx:confirm hook below (and dispatched "app-confirm" events),
// so every hx-confirm gets a themed, touch-friendly prompt for free.
function confirmHost() {
  return {
    show: false,
    question: "",
    onYes: null,
    open(d) {
      this.question = (d && d.question) || "Are you sure?";
      this.onYes = (d && d.onYes) || null;
      this.show = true;
      this.$nextTick(() => this.$refs.yes && this.$refs.yes.focus());
    },
    yes() {
      this.show = false;
      const fn = this.onYes;
      this.onYes = null;
      if (fn) fn();
    },
    no() {
      this.show = false;
      this.onYes = null;
    },
  };
}

function toast(message, level) {
  window.dispatchEvent(new CustomEvent("show-toast", { detail: { message, level } }));
}

// generateBarcode fills the barcode input in the button's .bc-field wrapper with
// a fresh, unused EAN-13 from the server. Dispatching "input" keeps any Alpine
// x-model (label-page preview) in sync. Reachable by any role via the session
// cookie (same-origin fetch).
async function generateBarcode(btn) {
  const input = btn.closest(".bc-field")?.querySelector("input");
  if (!input) return;
  btn.disabled = true;
  try {
    const res = await fetch("/api/products/barcode/generate", { credentials: "same-origin" });
    const json = await res.json();
    if (!res.ok || !json.success) {
      throw new Error(json?.error?.message || "Could not generate a barcode");
    }
    input.value = json.data.barcode;
    input.dispatchEvent(new Event("input", { bubbles: true }));
  } catch (e) {
    toast(e.message || "Could not generate a barcode", "error");
  } finally {
    btn.disabled = false;
  }
}
window.generateBarcode = generateBarcode;

// printBill sends a sale to the thermal printer server-side (ESC/POS via CUPS).
// This replaces opening the HTML receipt + window.print(), which a driverless
// raw thermal queue mis-prints as PDF garbage. apiFetch toasts on failure.
async function printBill(id) {
  try {
    await apiFetch("POST", "/cashier/print/" + id);
    toast("Receipt sent to printer", "success");
  } catch (_) {
    /* apiFetch already surfaced the error */
  }
}

// pos: the cashier terminal. Cart math here is a live preview only — the server
// recomputes every amount authoritatively when the sale is posted.
function pos(symbol, defaultType, promptAfterSale) {
  return {
    sym: symbol,
    // When false, completing a sale auto-prints the receipt and resets straight
    // to a new sale instead of showing the Print / New Sale prompt.
    promptAfterSale: promptAfterSale !== false,
    products: [],
    customers: [],
    cart: [],
    search: "",
    scan: "",
    saleType: defaultType || "retail",
    customerId: "",
    discount: 0,
    discountType: "fixed", // bill-level discount: "fixed" (Rs) or "percent" (%)
    // Split tender: one or more payment lines (cash / card / online).
    payments: [{ method: "cash", amount: 0, reference: "" }],
    busy: false,
    session: null,
    summary: null,
    denoms: [],
    openCounts: {},
    closeCounts: {},
    lastClosing: null,
    lastBreakdown: null,
    showClose: false,
    showWithdraw: false,
    withdrawAmount: 0,
    withdrawReason: "",
    showDeposit: false,
    depositAmount: 0,
    depositReason: "",
    showAddCustomer: false,
    newCustomer: { name: "", phone: "", credit_limit: "" },
    closeResult: null,
    receipt: null,
    // Parked carts (hold / resume).
    holds: [],
    showHolds: false,
    // Quick-add: sell an item that isn't in the catalog yet.
    showQuickItem: false,
    quickItem: { name: "", price: "", qty: 1, barcode: "", unit_id: 0 },
    units: [],

    async init() {
      await this.loadDenoms();
      await this.loadSummary();
      await this.loadProducts();
      await this.loadCustomers();
      await this.loadUnits();
      await this.loadHolds();
    },

    money(v) {
      const n = Number(v) || 0;
      return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    },
    money3(v) {
      const n = Number(v) || 0;
      return n.toLocaleString(undefined, { maximumFractionDigits: 3 });
    },

    // --- keyboard shortcuts (touch + keyboard revamp) ---
    // F2 search · F3 scan · F9 discount · F4 hold · F10 pay · Esc close/blur.
    focusEl(ref) {
      const el = this.$refs[ref];
      if (!el) return;
      el.focus();
      if (el.select) el.select();
    },
    anyModalOpen() {
      return this.showHolds || this.showWithdraw || this.showDeposit || this.showAddCustomer || this.showClose || this.showQuickItem || !!this.receipt;
    },
    closeModals() {
      this.showHolds = false;
      this.showWithdraw = false;
      this.showDeposit = false;
      this.showAddCustomer = false;
      this.showClose = false;
      this.showQuickItem = false;
      this.closeResult = null;
      this.receipt = null;
    },
    onKey(e) {
      // Let the global command palette own the keyboard while it's open.
      if (document.body.classList.contains("palette-open")) return;
      switch (e.key) {
        case "Escape":
          if (this.anyModalOpen()) {
            e.preventDefault();
            this.closeModals();
          } else if (document.activeElement && document.activeElement.blur) {
            document.activeElement.blur();
          }
          return;
        case "Enter":
          if (this.receipt) {
            e.preventDefault();
            this.newSale();
          }
          return;
        case "F2":
          e.preventDefault();
          this.focusEl("searchInput");
          return;
        case "F3":
          e.preventDefault();
          this.focusEl("scanInput");
          return;
        case "F9":
          e.preventDefault();
          this.focusEl("discountInput");
          return;
        case "F4":
          e.preventDefault();
          if (!this.anyModalOpen()) this.holdSale();
          return;
        case "F10":
          e.preventDefault();
          if (!this.anyModalOpen()) this.checkout();
          return;
      }
    },
    // Touch-friendly qty steppers for cart lines.
    incQty(it) {
      it.qty = (Number(it.qty) || 0) + 1;
      this.syncSerials(it);
    },
    decQty(it) {
      it.qty = Math.max(0, (Number(it.qty) || 0) - 1);
      this.syncSerials(it);
    },

    async loadProducts() {
      const q = encodeURIComponent(this.search);
      const json = await apiFetch("GET", `/api/products?limit=100&search=${q}`);
      this.products = json.data || [];
    },
    async loadCustomers() {
      try {
        const json = await apiFetch("GET", "/api/customers");
        this.customers = json.data || [];
      } catch (_) {
        this.customers = [];
      }
    },
    async loadUnits() {
      try {
        const json = await apiFetch("GET", "/api/units");
        this.units = json.data || [];
      } catch (_) {
        this.units = [];
      }
    },
    async loadDenoms() {
      try {
        const json = await apiFetch("GET", "/api/denominations");
        this.denoms = json.data || [];
      } catch (_) {
        this.denoms = [];
      }
    },
    async loadSummary() {
      try {
        const json = await apiFetch("GET", "/api/cash-register/summary");
        this.summary = json.data || null;
        this.session = (this.summary && this.summary.session) || null;
        this.lastClosing = (this.summary && this.summary.last_closing) || null;
        this.lastBreakdown = (this.summary && this.summary.last_breakdown) || null;
      } catch (_) {
        this.summary = null;
        this.session = null;
      }
    },
    // Sum value × qty over a {denomValue: count} map.
    countTotal(counts) {
      let t = 0;
      for (const d of this.denoms) t += Number(d.value) * Number(counts[d.value] || 0);
      return t;
    },
    buildBreakdown(counts) {
      const b = {};
      for (const d of this.denoms) {
        const q = Number(counts[d.value] || 0);
        if (q > 0) b[String(d.value)] = q;
      }
      return b;
    },
    // Reopen helper: prefill the opening count from last close (decoded from the
    // base64 JSON the API returns for the previous closing breakdown).
    useLastCounts() {
      this.openCounts = {};
      try {
        const json = JSON.parse(atob(this.lastBreakdown));
        for (const k in json) this.openCounts[k] = json[k];
      } catch (_) {
        /* no usable breakdown */
      }
    },
    clearCounts(which) {
      this[which] = {};
    },
    async openRegister() {
      const json = await apiFetch("POST", "/api/cash-register/open", {
        breakdown: this.buildBreakdown(this.openCounts),
      });
      this.session = json.data;
      this.openCounts = {};
      await this.loadSummary();
      toast("Register opened", "success");
    },
    startClose() {
      this.closeCounts = {};
      this.closeResult = null;
      this.showClose = true;
    },
    async submitClose() {
      const json = await apiFetch("POST", "/api/cash-register/close", {
        breakdown: this.buildBreakdown(this.closeCounts),
      });
      this.closeResult = json.data;
      this.session = null;
      await this.loadSummary();
    },
    async withdraw() {
      if (Number(this.withdrawAmount) <= 0) {
        toast("Enter an amount to withdraw", "error");
        return;
      }
      // Can't take out more than the drawer holds (server enforces this too).
      const available = Number(this.summary?.expected);
      if (!Number.isNaN(available) && Number(this.withdrawAmount) > available) {
        toast("Can't withdraw more than what's in the drawer", "error");
        return;
      }
      await apiFetch("POST", "/api/cash-register/withdraw", {
        amount: String(this.withdrawAmount),
        reason: this.withdrawReason,
      });
      this.showWithdraw = false;
      this.withdrawAmount = 0;
      this.withdrawReason = "";
      await this.loadSummary();
      toast("Cash withdrawn", "success");
    },
    async deposit() {
      if (Number(this.depositAmount) <= 0) {
        toast("Enter an amount to deposit", "error");
        return;
      }
      await apiFetch("POST", "/api/cash-register/pay-in", {
        amount: String(this.depositAmount),
        reason: this.depositReason,
      });
      this.showDeposit = false;
      this.depositAmount = 0;
      this.depositReason = "";
      await this.loadSummary();
      toast("Cash deposited", "success");
    },
    async addCustomer() {
      if (!this.newCustomer.name.trim()) {
        toast("Enter a customer name", "error");
        return;
      }
      const json = await apiFetch("POST", "/api/customers", {
        name: this.newCustomer.name.trim(),
        phone: this.newCustomer.phone.trim() || null,
        credit_limit: String(this.newCustomer.credit_limit || "0"),
      });
      await this.loadCustomers();
      this.customerId = String(json.data.id); // select the new customer
      this.showAddCustomer = false;
      this.newCustomer = { name: "", phone: "", credit_limit: "" };
      toast("Customer added", "success");
    },

    // --- hold / park sale ---
    async loadHolds() {
      try {
        const json = await apiFetch("GET", "/api/held-sales");
        this.holds = json.data || [];
      } catch (_) {
        this.holds = [];
      }
    },
    async holdSale() {
      if (this.cart.length === 0) {
        toast("Cart is empty", "error");
        return;
      }
      const label =
        (this.customerId &&
          (this.customers.find((c) => String(c.id) === String(this.customerId)) || {})
            .name) ||
        (this.cart[0] && this.cart[0].name) ||
        "";
      try {
        await apiFetch("POST", "/api/held-sales", {
          label: label,
          sale_type: this.saleType,
          customer_id: this.customerId ? Number(this.customerId) : null,
          discount: String(this.discount || 0),
          discount_type: this.discountType,
          cart: this.cart,
          item_count: this.cart.length,
          total: String(this.total()),
        });
        toast("Sale held", "success");
        this.newSale();
        await this.loadHolds();
      } catch (_) {
        /* toast already shown */
      }
    },
    async resumeHold(h) {
      this.cart = Array.isArray(h.cart) ? h.cart : [];
      this.saleType = h.sale_type || "retail";
      this.customerId = h.customer_id ? String(h.customer_id) : "";
      this.discount = Number(h.discount) || 0;
      this.discountType = h.discount_type || "fixed";
      this.payments = [{ method: "cash", amount: 0, reference: "" }];
      this.receipt = null;
      this.showHolds = false;
      await this.deleteHold(h.id, true);
    },
    async deleteHold(id, silent) {
      try {
        await apiFetch("DELETE", "/api/held-sales/" + id);
        await this.loadHolds();
        if (!silent) toast("Held sale removed", "success");
      } catch (_) {
        /* toast already shown */
      }
    },

    unitPriceFor(p) {
      if (this.saleType === "wholesale" && Number(p.wholesale_price) > 0) {
        return Number(p.wholesale_price);
      }
      return Number(p.selling_price);
    },
    addToCart(p) {
      const existing = this.cart.find((x) => x.id === p.id);
      if (existing) {
        existing.qty = Number(existing.qty) + 1;
        this.syncSerials(existing);
        return;
      }
      this.cart.push({
        id: p.id,
        name: p.name,
        unit_price: this.unitPriceFor(p),
        tax_rate: Number(p.tax_rate) || 0,
        qty: 1,
        stock: Number(p.stock_qty),
        // Weight/volume units (kg, g, ltr, ml) accept fractional quantities;
        // everything else is whole-only.
        allowDecimal: !!p.unit_allow_decimal,
        // Per-item discount: defaults to 0; the cashier sets it per line at the
        // counter. Fixed is PER UNIT (× qty); percent is off the line.
        discount: 0,
        discountType: "fixed",
        // Serial-tracked products need a unique serial per unit captured below.
        track_serial: !!p.track_serial,
        warranty_months: Number(p.warranty_months) || 0,
        serials: [],
      });
      this.syncSerials(this.cart[this.cart.length - 1]);
    },
    // syncSerials keeps a serial-tracked line's serial inputs in step with its
    // quantity (one box per unit), preserving anything already typed.
    syncSerials(it) {
      if (!it || !it.track_serial) return;
      const n = Math.max(0, Math.round(Number(it.qty) || 0));
      if (!Array.isArray(it.serials)) it.serials = [];
      while (it.serials.length < n) it.serials.push("");
      if (it.serials.length > n) it.serials.length = n;
    },
    async scanBarcode() {
      const code = this.scan.trim();
      if (!code) return;
      try {
        const json = await apiFetch("GET", `/api/products/barcode/${encodeURIComponent(code)}`, undefined, { silent: true });
        this.addToCart(json.data);
        this.scan = "";
      } catch (e) {
        this.scan = "";
        // Unknown barcode → offer to quick-add it (prefilled with the scanned code)
        // instead of a dead-end. Other errors still surface a toast.
        if (e && e.status === 404) {
          this.openQuickItem(code);
        } else {
          toast((e && e.message) || "Lookup failed", "error");
        }
      }
    },
    // --- Quick item (sell something not yet in the catalog) ---
    // Default the unit to "pcs" if present, else the first unit.
    defaultUnitId() {
      const pcs = this.units.find((u) => u.abbreviation === "pcs");
      return (pcs || this.units[0] || {}).id || 0;
    },
    quickUnitAllowsDecimal() {
      const u = this.units.find((x) => x.id === this.quickItem.unit_id);
      return !!(u && u.allow_decimal);
    },
    openQuickItem(barcode) {
      this.quickItem = { name: "", price: "", qty: 1, barcode: barcode || "", unit_id: this.defaultUnitId() };
      this.showQuickItem = true;
      this.$nextTick(() => this.$refs.quickItemName && this.$refs.quickItemName.focus());
    },
    async genQuickBarcode() {
      try {
        const json = await apiFetch("GET", "/api/products/barcode/generate");
        this.quickItem.barcode = json.data.barcode;
      } catch (_) {
        /* apiFetch already toasted */
      }
    },
    async submitQuickItem() {
      const name = (this.quickItem.name || "").trim();
      if (!name) {
        toast("Enter an item name", "error");
        return;
      }
      if (!(Number(this.quickItem.price) >= 0) || this.quickItem.price === "") {
        toast("Enter a price", "error");
        return;
      }
      let qty = Number(this.quickItem.qty) || 0;
      if (!this.quickUnitAllowsDecimal()) qty = Math.round(qty);
      if (!(qty > 0)) qty = 1;
      try {
        const json = await apiFetch("POST", "/cashier/quick-item", {
          name,
          price: String(this.quickItem.price),
          qty: String(qty),
          barcode: (this.quickItem.barcode || "").trim(),
          unit_id: Number(this.quickItem.unit_id) || 0,
        });
        this.addToCart(json.data);
        // Reflect the entered quantity (addToCart starts a new line at qty 1).
        const line = this.cart[this.cart.length - 1];
        if (line) {
          line.qty = qty;
          this.syncSerials(line);
        }
        this.showQuickItem = false;
        toast("Item added", "success");
        await this.loadProducts();
      } catch (_) {
        /* apiFetch already toasted */
      }
    },
    clampQty(it) {
      let q = Number(it.qty) || 0;
      if (q < 0) q = 0;
      // Whole-only units snap to integers; weighed units keep their decimals.
      if (!it.allowDecimal) q = Math.round(q);
      it.qty = q;
      this.syncSerials(it);
    },
    removeItem(idx) {
      this.cart.splice(idx, 1);
    },
    // lineGross is qty × unit price (before any discount).
    lineGross(it) {
      return (Number(it.qty) || 0) * (Number(it.unit_price) || 0);
    },
    // lineDiscount mirrors the server: fixed is per-unit (× qty), percent is off
    // the line gross; clamped to the line gross.
    lineDiscount(it) {
      const g = this.lineGross(it);
      const v = Number(it.discount) || 0;
      let d = it.discountType === "percent" ? (g * v) / 100 : v * (Number(it.qty) || 0);
      if (d < 0) d = 0;
      if (d > g) d = g;
      return d;
    },
    lineNet(it) {
      return this.lineGross(it) - this.lineDiscount(it);
    },
    // lineTotal kept as the post-discount line value (used by the cart display).
    lineTotal(it) {
      return this.lineNet(it);
    },
    subtotal() {
      return this.cart.reduce((s, it) => s + this.lineGross(it), 0);
    },
    itemDiscountTotal() {
      return this.cart.reduce((s, it) => s + this.lineDiscount(it), 0);
    },
    // Bill discount resolved against the pre-tax net (after item discounts).
    billDiscount() {
      const base = this.subtotal() - this.itemDiscountTotal();
      const v = Number(this.discount) || 0;
      let d = this.discountType === "percent" ? (base * v) / 100 : v;
      if (d < 0) d = 0;
      if (d > base) d = base;
      return d;
    },
    // Tax mirrors the server: charged on the discounted line net.
    taxTotal() {
      return this.cart.reduce((s, it) => s + this.lineNet(it) * (it.tax_rate / 100), 0);
    },
    total() {
      return Math.max(
        0,
        this.subtotal() - this.itemDiscountTotal() - this.billDiscount() + this.taxTotal()
      );
    },

    // --- split-tender payments ---
    addPayment() {
      this.payments.push({ method: "card", amount: 0, reference: "" });
    },
    removePayment(idx) {
      this.payments.splice(idx, 1);
      if (this.payments.length === 0) {
        this.payments.push({ method: "cash", amount: 0, reference: "" });
      }
    },
    paidTotal() {
      return this.payments.reduce((s, p) => s + (Number(p.amount) || 0), 0);
    },
    changeDue() {
      return Math.max(0, this.paidTotal() - this.total());
    },
    // Advisory greedy breakdown of the change due into available denominations,
    // high→low. Stateless suggestion only — we don't track the live drawer mix,
    // so the cashier overrides if a note isn't on hand. Returns [{value, qty}].
    changeNotes() {
      let rem = Math.round(this.changeDue() * 100); // work in cents to avoid float drift
      const out = [];
      for (const d of this.denoms) {
        const cents = Math.round(Number(d.value) * 100);
        if (cents <= 0) continue;
        const qty = Math.floor(rem / cents);
        if (qty > 0) { out.push({ value: Number(d.value), qty }); rem -= qty * cents; }
      }
      return out; // any sub-smallest remainder is simply not representable; ignore
    },
    balanceDue() {
      return Math.max(0, this.total() - this.paidTotal());
    },
    // Fill a payment line with the still-unpaid balance (quick "exact" button).
    fillRemaining(idx) {
      const others = this.payments.reduce(
        (s, p, i) => (i === idx ? s : s + (Number(p.amount) || 0)),
        0
      );
      this.payments[idx].amount = Math.max(0, this.total() - others);
    },

    async checkout() {
      if (this.cart.length === 0 || this.busy) return;
      // Serial-tracked lines need a serial for every unit before we can sell.
      for (const it of this.cart) {
        if (!it.track_serial) continue;
        const n = Math.max(0, Math.round(Number(it.qty) || 0));
        const filled = (it.serials || []).filter((s) => String(s || "").trim() !== "").length;
        if (filled < n) {
          toast(`Enter ${n} serial number(s) for ${it.name}`, "error");
          return;
        }
      }
      this.busy = true;
      try {
        const payload = {
          sale_type: this.saleType,
          customer_id: this.customerId ? Number(this.customerId) : null,
          discount: String(this.discount || 0),
          discount_type: this.discountType,
          items: this.cart.map((it) => ({
            product_id: it.id,
            quantity: String(it.qty),
            discount: String(it.discount || 0),
            discount_type: it.discountType || "fixed",
            serials: it.track_serial
              ? (it.serials || []).map((s) => String(s || "").trim())
              : [],
          })),
          payments: this.payments
            .filter((p) => Number(p.amount) > 0)
            .map((p) => ({
              method: p.method,
              amount: String(p.amount),
              reference: p.reference ? String(p.reference) : null,
            })),
        };
        const json = await apiFetch("POST", "/api/sales", payload);
        toast("Sale complete", "success");
        if (this.promptAfterSale) {
          this.receipt = json.data; // show the Print / New Sale prompt
        } else {
          await printBill(json.data.sale.id); // auto-print (async, self-toasts)
          this.newSale(); // reset cart for the next customer
        }
        await this.loadProducts();
        await this.loadSummary();
      } catch (_) {
        /* toast already shown */
      } finally {
        this.busy = false;
      }
    },
    printReceipt() {
      if (!this.receipt) return;
      printBill(this.receipt.sale.id);
    },
    newSale() {
      this.cart = [];
      this.discount = 0;
      this.discountType = "fixed";
      this.payments = [{ method: "cash", amount: 0, reference: "" }];
      this.customerId = "";
      this.receipt = null;
    },
  };
}

// grn: the Goods Received Note (purchase entry) screen. Mirrors pos(): line math
// here is a preview only — the server recomputes the totals authoritatively.
function grn(symbol) {
  return {
    sym: symbol,
    supplierId: "",
    invoiceNo: "",
    dueDate: "",
    paid: 0,
    notes: "",
    lines: [],
    busy: false,

    init() {
      this.addLine();
    },
    money(v) {
      const n = Number(v) || 0;
      return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    },
    addLine() {
      this.lines.push({ product_id: 0, quantity: 0, cost_price: 0, selling_price: 0, expiry_date: "" });
    },
    removeLine(i) {
      this.lines.splice(i, 1);
      if (this.lines.length === 0) this.addLine();
    },
    // F4 add line · F10 receive. Touch steppers on the qty column.
    onKey(e) {
      if (document.body.classList.contains("palette-open")) return;
      if (e.key === "F4") {
        e.preventDefault();
        this.addLine();
      } else if (e.key === "F10") {
        e.preventDefault();
        this.submit();
      }
    },
    incLine(l) {
      l.quantity = (Number(l.quantity) || 0) + 1;
    },
    decLine(l) {
      l.quantity = Math.max(0, (Number(l.quantity) || 0) - 1);
    },
    lineSub(l) {
      return (Number(l.quantity) || 0) * (Number(l.cost_price) || 0);
    },
    subtotal() {
      return this.lines.reduce((s, l) => s + this.lineSub(l), 0);
    },
    async submit() {
      if (this.busy) return;
      if (!this.supplierId) {
        toast("Select a supplier", "error");
        return;
      }
      const items = this.lines
        .filter((l) => Number(l.product_id) > 0 && Number(l.quantity) > 0)
        .map((l) => ({
          product_id: Number(l.product_id),
          quantity: String(l.quantity),
          cost_price: String(l.cost_price || 0),
          selling_price: String(l.selling_price || 0),
          expiry_date: l.expiry_date || "",
        }));
      if (items.length === 0) {
        toast("Add at least one line", "error");
        return;
      }
      this.busy = true;
      try {
        await apiFetch("POST", "/api/purchases", {
          supplier_id: Number(this.supplierId),
          invoice_no: this.invoiceNo || null,
          discount: "0",
          paid_amount: String(this.paid || 0),
          due_date: this.dueDate || "",
          notes: this.notes || null,
          items: items,
        });
        toast("Goods received", "success");
        window.location = "/admin/purchases";
      } catch (_) {
        /* toast already shown */
      } finally {
        this.busy = false;
      }
    },
  };
}

// pret: purchase-return (debit note) entry. Posts to /api/purchase-returns.
function pret(symbol) {
  return {
    sym: symbol,
    supplierId: "",
    reference: "",
    reason: "",
    lines: [],
    busy: false,
    init() {
      this.addLine();
    },
    money(v) {
      const n = Number(v) || 0;
      return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    },
    addLine() {
      this.lines.push({ product_id: 0, quantity: 0, cost_price: 0 });
    },
    removeLine(i) {
      this.lines.splice(i, 1);
      if (this.lines.length === 0) this.addLine();
    },
    // F4 add line · F10 submit return. Touch steppers on the qty column.
    onKey(e) {
      if (document.body.classList.contains("palette-open")) return;
      if (e.key === "F4") {
        e.preventDefault();
        this.addLine();
      } else if (e.key === "F10") {
        e.preventDefault();
        this.submit();
      }
    },
    incLine(l) {
      l.quantity = (Number(l.quantity) || 0) + 1;
    },
    decLine(l) {
      l.quantity = Math.max(0, (Number(l.quantity) || 0) - 1);
    },
    lineSub(l) {
      return (Number(l.quantity) || 0) * (Number(l.cost_price) || 0);
    },
    subtotal() {
      return this.lines.reduce((s, l) => s + this.lineSub(l), 0);
    },
    async submit() {
      if (this.busy) return;
      if (!this.supplierId) {
        toast("Select a supplier", "error");
        return;
      }
      const items = this.lines
        .filter((l) => Number(l.product_id) > 0 && Number(l.quantity) > 0)
        .map((l) => ({
          product_id: Number(l.product_id),
          quantity: String(l.quantity),
          cost_price: String(l.cost_price || 0),
        }));
      if (items.length === 0) {
        toast("Add at least one line", "error");
        return;
      }
      this.busy = true;
      try {
        await apiFetch("POST", "/api/purchase-returns", {
          supplier_id: Number(this.supplierId),
          reference: this.reference || null,
          reason: this.reason || null,
          items: items,
        });
        toast("Goods returned to supplier", "success");
        window.location = "/admin/purchase-returns";
      } catch (_) {
        /* toast already shown */
      } finally {
        this.busy = false;
      }
    },
  };
}

// saleReturn: per-line partial-return modal. Collects {sale_item_id: qty} and
// posts to /api/sales/:id/partial-return.
function saleReturn(saleId, opts) {
  opts = opts || {};
  const endpoint = opts.endpoint || "/api/sales/" + saleId + "/partial-return";
  const reloadEvent = opts.reload || "reload-sales";
  return {
    saleId: saleId,
    qtys: {},
    reason: "",
    busy: false,
    setQty(itemId, value, max) {
      let q = Number(value) || 0;
      if (q < 0) q = 0;
      if (q > max) q = max;
      this.qtys[itemId] = q;
    },
    async submit() {
      if (this.busy) return;
      const lines = Object.keys(this.qtys)
        .filter((k) => Number(this.qtys[k]) > 0)
        .map((k) => ({ sale_item_id: Number(k), quantity: String(this.qtys[k]) }));
      if (lines.length === 0) {
        toast("Enter at least one quantity to return", "error");
        return;
      }
      this.busy = true;
      try {
        await apiFetch("POST", endpoint, {
          reason: this.reason || null,
          lines: lines,
        });
        toast("Return processed & restocked", "success");
        window.dispatchEvent(new CustomEvent("close-modal"));
        document.body.dispatchEvent(new CustomEvent(reloadEvent));
      } catch (_) {
        /* toast already shown */
      } finally {
        this.busy = false;
      }
    },
  };
}

// labels: live barcode preview for the Barcode Labels page (product + custom).
function labels(sym) {
  return {
    sym: sym,
    // product form
    pName: "",
    pCode: "",
    pPrice: "",
    pShowPrice: true,
    // custom form
    cCode: "",
    cText: "",
    cPrice: "",
    cFormat: "CODE128",
    cShowPrice: false,

    onProduct(ev) {
      const o = ev.target.selectedOptions[0];
      this.pName = (o && o.dataset.name) || "";
      this.pCode = (o && o.dataset.code) || "";
      this.pPrice = (o && o.dataset.price) || "";
      this.$nextTick(() => this.draw("preview-product", this.pCode, "CODE128"));
    },
    renderCustom() {
      this.$nextTick(() => this.draw("preview-custom", this.cCode, this.cFormat));
    },
    draw(id, code, format) {
      const el = document.getElementById(id);
      if (!el) return;
      if (!code) {
        el.innerHTML = "";
        return;
      }
      try {
        JsBarcode(el, code, {
          format: format || "CODE128",
          displayValue: true,
          fontSize: 12,
          height: 38,
          margin: 2,
          width: 1.4,
        });
      } catch (e) {
        // invalid code for the chosen format — clear the preview
        el.innerHTML = "";
      }
    },
  };
}

// login: PIN-pad state for the login screen.
function login() {
  return {
    phone: "",
    pin: "",
    tap(d) {
      if (this.pin.length < 6) this.pin += d;
    },
    back() {
      this.pin = this.pin.slice(0, -1);
    },
  };
}

// themeToggle: light ⇄ dark switch. The class lives on <html> (set pre-paint by
// the inline script in Base); we just flip it and remember the choice.
function themeToggle() {
  return {
    dark: document.documentElement.classList.contains("dark"),
    toggle() {
      this.dark = !this.dark;
      document.documentElement.classList.toggle("dark", this.dark);
      try {
        localStorage.setItem("theme", this.dark ? "dark" : "light");
      } catch (e) {
        /* storage unavailable */
      }
    },
  };
}

// cmdPalette: keyboard-first "jump to any page" overlay (Cmd/Ctrl+K, or the
// search button). Type to filter, ↑/↓ to move, Enter to go, Esc to close.
function cmdPalette(items) {
  return {
    show: false,
    q: "",
    sel: 0,
    items: items || [],
    open() {
      this.show = true;
      this.q = "";
      this.sel = 0;
      document.body.classList.add("palette-open");
      this.$nextTick(() => this.$refs.input && this.$refs.input.focus());
    },
    close() {
      this.show = false;
      document.body.classList.remove("palette-open");
    },
    // "/" opens the palette from anywhere — but only when not typing into a field.
    onSlash(e) {
      if (e.key !== "/" || this.show) return;
      const t = e.target;
      const tag = t && t.tagName;
      if (
        tag === "INPUT" ||
        tag === "TEXTAREA" ||
        tag === "SELECT" ||
        (t && t.isContentEditable)
      ) {
        return;
      }
      e.preventDefault();
      this.open();
    },
    get filtered() {
      const q = this.q.trim().toLowerCase();
      if (!q) return this.items;
      return this.items.filter(
        (i) =>
          i.label.toLowerCase().includes(q) ||
          (i.group || "").toLowerCase().includes(q),
      );
    },
    move(d) {
      const n = this.filtered.length;
      if (!n) return;
      this.sel = (this.sel + d + n) % n;
    },
    go() {
      const it = this.filtered[this.sel];
      if (it) window.location.href = it.href;
    },
  };
}

// Searchable, hierarchy-aware category picker. Replaces a native <select> that
// can't show nesting (browsers collapse leading spaces in <option>) and gets
// unwieldy with many categories. Backs a hidden <input> so it submits like a
// normal field. cfg = { name, selected, options:[{id,name,depth}], includeAll,
// allLabel, reload }. When reload is true (the products filter), picking
// dispatches a bubbling "category-changed" event the filter form listens for.
function categoryPicker(cfg) {
  return {
    open: false,
    query: "",
    selected: cfg.selected || "",
    options: cfg.options || [],
    includeAll: !!cfg.includeAll,
    allLabel: cfg.allLabel || "All categories",
    reload: !!cfg.reload,
    placeholder: cfg.includeAll ? cfg.allLabel || "All categories" : "Select category…",
    filtered() {
      const q = this.query.trim().toLowerCase();
      if (!q) return this.options;
      return this.options.filter((o) => o.name.toLowerCase().includes(q));
    },
    label() {
      const o = this.options.find((o) => String(o.id) === String(this.selected));
      return o ? o.name : this.placeholder;
    },
    indent(o) {
      return "padding-left:" + (0.75 + o.depth * 1.1) + "rem";
    },
    toggle() {
      this.open = !this.open;
      if (this.open) this.$nextTick(() => this.$refs.search && this.$refs.search.focus());
    },
    pick(o) {
      this.selected = o ? String(o.id) : "";
      this.open = false;
      this.query = "";
      if (this.reload) this.$nextTick(() => this.$dispatch("category-changed"));
    },
    clear() {
      this.pick(null);
    },
  };
}

// Collapsible category tree for the admin Categories table. State is purely the
// set of expanded IDs; a row is visible when every ancestor in its path is
// expanded — so the default (nothing expanded) shows only top-level rows, and
// the logic survives HTMX innerHTML swaps with no DOM map to rebuild.
function categoryTree() {
  return {
    expanded: {},
    toggle(id) {
      this.expanded[id] = !this.expanded[id];
    },
    visible(path) {
      return (path || []).every((id) => this.expanded[id]);
    },
  };
}
