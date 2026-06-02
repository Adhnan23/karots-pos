// Global Alpine components and HTMX glue. Loaded before Alpine so the
// component factory functions exist when Alpine initializes.

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
async function apiFetch(method, url, body) {
  const opts = { method, headers: {}, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(url, opts);
  let json = null;
  try {
    json = await res.json();
  } catch (_) {
    /* no body */
  }
  if (!res.ok) {
    const msg = (json && json.error && json.error.message) || "Request failed";
    window.dispatchEvent(
      new CustomEvent("show-toast", { detail: { message: msg, level: "error" } })
    );
    throw new Error(msg);
  }
  return json;
}

function toast(message, level) {
  window.dispatchEvent(new CustomEvent("show-toast", { detail: { message, level } }));
}

// pos: the cashier terminal. Cart math here is a live preview only — the server
// recomputes every amount authoritatively when the sale is posted.
function pos(symbol, defaultType) {
  return {
    sym: symbol,
    products: [],
    customers: [],
    cart: [],
    search: "",
    scan: "",
    saleType: defaultType || "retail",
    customerId: "",
    discount: 0,
    cash: 0,
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
    closeResult: null,
    receipt: null,

    async init() {
      await this.loadDenoms();
      await this.loadSummary();
      await this.loadProducts();
      await this.loadCustomers();
    },

    money(v) {
      const n = Number(v) || 0;
      return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    },
    money3(v) {
      const n = Number(v) || 0;
      return n.toLocaleString(undefined, { maximumFractionDigits: 3 });
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
        return;
      }
      this.cart.push({
        id: p.id,
        name: p.name,
        unit_price: this.unitPriceFor(p),
        tax_rate: Number(p.tax_rate) || 0,
        qty: 1,
        stock: Number(p.stock_qty),
      });
    },
    async scanBarcode() {
      const code = this.scan.trim();
      if (!code) return;
      try {
        const json = await apiFetch("GET", `/api/products/barcode/${encodeURIComponent(code)}`);
        this.addToCart(json.data);
        this.scan = "";
      } catch (_) {
        this.scan = "";
      }
    },
    clampQty(it) {
      if (Number(it.qty) < 0) it.qty = 0;
    },
    removeItem(idx) {
      this.cart.splice(idx, 1);
    },
    lineTotal(it) {
      return (Number(it.qty) || 0) * (Number(it.unit_price) || 0);
    },
    subtotal() {
      return this.cart.reduce((s, it) => s + this.lineTotal(it), 0);
    },
    taxTotal() {
      return this.cart.reduce((s, it) => s + this.lineTotal(it) * (it.tax_rate / 100), 0);
    },
    total() {
      return Math.max(0, this.subtotal() - (Number(this.discount) || 0) + this.taxTotal());
    },

    async checkout() {
      if (this.cart.length === 0 || this.busy) return;
      this.busy = true;
      try {
        const payload = {
          sale_type: this.saleType,
          customer_id: this.customerId ? Number(this.customerId) : null,
          discount: String(this.discount || 0),
          items: this.cart.map((it) => ({
            product_id: it.id,
            quantity: String(it.qty),
            discount: "0",
          })),
          payments:
            Number(this.cash) > 0
              ? [{ method: "cash", amount: String(this.cash) }]
              : [],
        };
        const json = await apiFetch("POST", "/api/sales", payload);
        this.receipt = json.data;
        toast("Sale complete", "success");
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
      window.open("/cashier/receipt/" + this.receipt.sale.id + "?print=1", "_blank");
    },
    newSale() {
      this.cart = [];
      this.discount = 0;
      this.cash = 0;
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
