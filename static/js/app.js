// Global Alpine components and HTMX glue. Loaded before Alpine so the
// component factory functions exist when Alpine initializes.

// Keyboard affordance: when HTMX swaps a form into the modal container, focus
// its first field so keyboard users can type immediately (Esc closes it — see
// ModalHost). Harmless on touch.
document.addEventListener("DOMContentLoaded", function () {
  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (!e.target || e.target.id !== "modal-container") return;
    // Alpine's MutationObserver normally initialises an HTMX-swapped fragment,
    // but can miss it (e.g. an unrelated effect threw mid-flush), leaving the
    // modal's Cancel / ✕ handlers unbound so only Esc closes it. Wait one frame
    // so the observer has run, then init only the x-data elements it left
    // uninitialised — PER ELEMENT and guarded, because initialising an already-
    // initialised subtree re-runs every x-for and duplicates option lists
    // (e.g. the category picker showed each category twice).
    requestAnimationFrame(function () {
      if (window.Alpine) {
        e.target.querySelectorAll("[x-data]").forEach(function (el) {
          if (!el._x_dataStack) window.Alpine.initTree(el);
        });
      }
      const el = e.target.querySelector(
        "input:not([type=hidden]):not([type=checkbox]):not([type=radio]), select, textarea",
      );
      if (el) el.focus();
    });
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
  // Bridge the server's "money-print" HX-Trigger (fired on the request element)
  // to a window event the global print-prompt host listens for. Done before the
  // triggering modal/form is torn down so the detail survives.
  document.body.addEventListener("money-print", function (e) {
    window.dispatchEvent(new CustomEvent("app-print-prompt", { detail: e.detail || {} }));
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

// printMoneySlip sends a CR- money receipt (till open/close/withdraw/pay-in and
// other cash moves) to the thermal printer, mirroring printBill for sales. The
// server re-loads the receipt (resolving the operator name) and prints it.
async function printMoneySlip(id) {
  if (!id) return;
  await postPrint("/cashier/money-receipts/" + id + "/print");
}

// postPrint POSTs to a slip-reprint endpoint (cashier or admin money receipt).
// The endpoint returns 200 and emits its own success/failure toast via HX-Trigger
// when hit by HTMX; over fetch we just confirm it was sent.
async function postPrint(url) {
  if (!url) return;
  try {
    await apiFetch("POST", url);
    toast("Receipt sent to printer", "success");
  } catch (_) {
    /* apiFetch already surfaced the error */
  }
}

// printPromptHost backs the shared "Print receipt?" prompt shown after a money
// move when "ask before printing" is on. Opened via the "app-print-prompt"
// window event with { url, reload }. Print sends the slip; Skip dismisses; either
// reloads the page afterwards when reload was requested (admin balance views).
function printPromptHost() {
  return {
    show: false,
    url: "",
    reload: false,
    open(d) {
      this.url = (d && d.url) || "";
      this.reload = !!(d && d.reload);
      this.show = true;
    },
    async doPrint() {
      this.show = false;
      await postPrint(this.url);
      if (this.reload) this.reloadSoon();
    },
    doSkip() {
      this.show = false;
      if (this.reload) this.reloadSoon();
    },
    reloadSoon() {
      setTimeout(() => location.reload(), 500);
    },
  };
}

// pos: the cashier terminal. Cart math here is a live preview only — the server
// recomputes every amount authoritatively when the sale is posted.
function pos(symbol, defaultType, askToPrint, pluginRoots, drawerSections) {
  return {
    sym: symbol,
    // When false, completing a sale auto-prints the receipt and resets straight
    // to a new sale instead of showing the Print / New Sale prompt.
    askToPrint: askToPrint !== false,
    products: [],
    // Cashier menu: top-level + drill-down group cards (replaces the old default
    // grid). groupStack is the breadcrumb of {id,name,emoji[,id|url]} from root
    // to here — core groups carry `.id`, plugin folders carry `.url` instead.
    groupChildren: [],
    groupStack: [],
    inGroups: true,
    // Plugin menu roots (e.g. "Reload & Bills"), injected server-side from
    // plugin.CashierMenuRoots(). Rendered as cards alongside product groups at
    // the top of the menu; tapping one drills into the menu-node protocol.
    pluginRoots: pluginRoots || [],
    // Plugin drawer sections (e.g. recharge float): input fragments loaded into the
    // till Open/Close dialogs, saved to each section's endpoint around the drawer call.
    drawerSections: drawerSections || [],
    // menuMode drives which of the 3 card/step views is shown inside the same
    // drill-down region: the group/plugin card grids, an inline amount-entry
    // step (plugin 'amount' leaves), or an inline HTML detail fragment
    // (plugin 'detail' leaves).
    menuMode: "cards", // "cards" | "amount" | "detail"
    amountNode: null,
    amountValue: "",
    amountError: "",
    detailHtml: "",
    pluginLeaves: [], // leaf nodes of the current plugin folder, rendered as cards
    _leaves: [],
    customers: [],
    cart: [],
    search: "",
    scan: "",
    saleType: defaultType || "retail",
    customerId: "",
    custSearch: "", // searchable customer chooser (filters the loaded list)
    custOpen: false,
    discount: 0,
    discountType: "fixed", // bill-level discount: "fixed" (Rs) or "percent" (%)
    // Split tender: one or more payment lines (cash / card / online / wallet).
    // No method is pre-selected — the cashier picks one per sale (see selectMethod).
    payments: [{ method: "", amount: 0, reference: "", deviceId: "" }],
    walletDevices: [], // recharge plugin tender: devices a wallet payment can credit (with live balance)
    busy: false,
    session: null,
    summary: null,
    denoms: [],
    openCounts: {},
    closeCounts: {},
    lastClosing: null,
    lastBreakdown: null,
    showClose: false,
    // Set when the page was opened via /cashier?logout=1 — the user tried to log
    // out with the till still open, so we force the close/count flow and, once it
    // succeeds, send them on to /logout instead of back to the POS.
    logoutMode: false,
    showWithdraw: false,
    withdrawAmount: 0,
    withdrawReason: "",
    showDeposit: false,
    depositAmount: 0,
    depositReason: "",
    // Cash lockers (vault/safe) the drawer can draw float from / bank cash to.
    lockers: [],
    openLockerId: "",
    closeLockerId: "",
    withdrawLockerId: "",
    depositLockerId: "",
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
      await this.loadLockers();
      if (!this.session) await this.loadDrawerSections("open");
      // Logout was blocked because the till is still open: jump straight to the
      // count/close dialog. If the session somehow closed already, just finish
      // logging out so the user isn't stranded.
      this.logoutMode = new URLSearchParams(location.search).get("logout") === "1";
      if (this.logoutMode) {
        if (this.session) this.startClose();
        else window.location.assign("/logout");
      }
    },

    // Cash lockers available as a float source (drawer open / pay-in) or
    // destination (close / withdraw). Best-effort: an empty list just hides the
    // pickers and the drawer behaves exactly as before.
    async loadLockers() {
      try {
        const json = await apiFetch("GET", "/cashier/lockers");
        this.lockers = json.data || [];
      } catch (e) {
        this.lockers = [];
      }
    },

    // Load each plugin drawer section's input fragment into the Open ('open') or
    // Close ('close') dialog slot. Fragments are plain inputs (no hx-*), so no
    // Alpine/HTMX init is needed — saveDrawerSections reads their values directly.
    async loadDrawerSections(which) {
      const box = which === "open" ? this.$refs.openSections : this.$refs.closeSections;
      if (!box) return;
      // Build into a detached fragment and swap it in atomically at the END, not
      // by clearing up front — two concurrent calls (Alpine re-init, logout +
      // manual close) would otherwise both pass an early clear and each append,
      // rendering every device twice. Building fresh and replacing last-wins.
      const frag = document.createDocumentFragment();
      for (const s of this.drawerSections) {
        const url = which === "open" ? s.openFormUrl : s.closeFormUrl;
        if (!url) continue;
        try {
          const res = await fetch(url, { credentials: "same-origin" });
          if (!res.ok) continue;
          const wrap = document.createElement("div");
          wrap.setAttribute("data-drawer-save", which === "open" ? s.saveOpenUrl : s.saveCloseUrl);
          wrap.innerHTML = await res.text();
          frag.appendChild(wrap);
        } catch (_) {
          /* a missing section just doesn't render */
        }
      }
      box.replaceChildren(frag);
    },
    // POST each loaded section's inputs (form-encoded) to its save URL. Returns
    // false if any 'close' save failed (caller aborts the till close). 'open'
    // saves are best-effort (reconciliation auto-carries the last close).
    async saveDrawerSections(which) {
      const box = which === "open" ? this.$refs.openSections : this.$refs.closeSections;
      if (!box) return true;
      const wraps = box.querySelectorAll("[data-drawer-save]");
      for (const w of wraps) {
        const url = w.getAttribute("data-drawer-save");
        if (!url) continue;
        const params = new URLSearchParams();
        w.querySelectorAll("input[name], select[name], textarea[name]").forEach((el) => {
          if (String(el.value).trim() !== "") params.append(el.name, el.value);
        });
        try {
          const res = await fetch(url, {
            method: "POST",
            credentials: "same-origin",
            headers: { "Content-Type": "application/x-www-form-urlencoded" },
            body: params.toString(),
          });
          if (!res.ok && which === "close") {
            const j = await res.json().catch(() => ({}));
            toast((j.error && j.error.message) || "Could not save float counts", "error");
            return false;
          }
        } catch (_) {
          if (which === "close") {
            toast("Could not save float counts", "error");
            return false;
          }
        }
      }
      return true;
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
      // Typing overrides the group menu: show flat name/barcode search results.
      if (this.search && this.search.trim()) {
        this.inGroups = false;
        const q = encodeURIComponent(this.search);
        const json = await apiFetch("GET", `/api/products?limit=100&search=${q}`);
        this.products = json.data || [];
        this.groupChildren = [];
        return;
      }
      // No search: show the group level we're on (or the top level).
      this.inGroups = true;
      const top = this.groupStack[this.groupStack.length - 1];
      if (!top) return this.loadGroupsTop();
      if (top.url) return this.fetchNodes(top.url);
      return this.openGroup(top.id, true);
    },
    async loadGroupsTop() {
      this.groupStack = [];
      this.menuMode = "cards";
      this.pluginLeaves = [];
      const json = await apiFetch("GET", "/api/groups");
      this.groupChildren = (json.data && json.data.groups) || [];
      this.products = [];
    },
    async openGroup(id, reload) {
      const json = await apiFetch("GET", `/api/groups/${id}`);
      const d = json.data || {};
      this.inGroups = true;
      this.menuMode = "cards";
      this.pluginLeaves = [];
      if (!reload) {
        this.groupStack = (d.breadcrumb || []).map((g) => ({
          id: g.id, name: g.name, emoji: g.emoji,
        }));
      }
      this.groupChildren = d.children || [];
      this.products = d.products || [];
    },
    // --- Plugin menu-node protocol -----------------------------------------
    // A plugin root's ChildrenURL (and every folder/leaf it returns) speaks a
    // small JSON protocol: {"nodes":[{kind:"folder",...}|{kind:"leaf",...}]}.
    // Folders behave like core product groups (pushed onto groupStack, drilled
    // into via fetchNodes) but carry `.url` instead of `.id` so backGroup knows
    // to re-fetch rather than call the core /api/groups/:id endpoint. Leaves
    // render as cards that open one of two inline steps (amount entry, or an
    // HTML detail fragment) or add straight to the cart (kind "product").
    openPluginRoot(r) {
      this.inGroups = true;
      this.groupStack = [{ name: r.label, emoji: r.emoji, url: r.url }];
      return this.fetchNodes(r.url);
    },
    async fetchNodes(url) {
      this.menuMode = "cards";
      const json = await apiFetch("GET", url);
      const nodes = (json && json.nodes) || (json.data && json.data.nodes) || [];
      // Map nodes onto the existing card grids: folders -> groupChildren, leaves kept on the node.
      this.groupChildren = nodes
        .filter((n) => n.kind === "folder")
        .map((n) => ({
          name: n.name,
          emoji: n.emoji,
          _node: n,
          _key: "p" + (n.children_url || n.name),
        }));
      this.products = []; // plugin folders have no product leaves
      this._leaves = nodes.filter((n) => n.kind === "leaf");
      this.pluginLeaves = this._leaves; // rendered as cards
    },
    openNode(node) {
      if (node.kind === "folder") {
        this.groupStack.push({ name: node.name, emoji: node.emoji, url: node.children_url });
        return this.fetchNodes(node.children_url);
      }
      if (node.action === "amount") {
        this.amountNode = node;
        this.amountValue = "";
        this.amountError = "";
        this.menuMode = "amount";
        this.$nextTick(() => this.$refs.amtInput && this.$refs.amtInput.focus());
        return;
      }
      if (node.action === "detail") {
        return this.openDetail(node.detail_url);
      }
      if (node.action === "product") return this.addToCart(node.product);
    },
    async openDetail(url) {
      const res = await fetch(url, { credentials: "same-origin" });
      this.detailHtml = await res.text();
      this.menuMode = "detail";
      this.$nextTick(() =>
        window.Alpine &&
        window.Alpine.initTree &&
        this.$refs.detailBox &&
        window.Alpine.initTree(this.$refs.detailBox)
      );
    },
    async confirmAmount() {
      if (this.busy) return;
      this.busy = true;
      this.amountError = "";
      try {
        const res = await fetch(this.amountNode.add_url, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json", "Accept": "application/json" },
          body: JSON.stringify({ amount: this.amountValue, meta: this.amountNode.meta || {} }),
        });
        const json = await res.json().catch(() => ({}));
        if (!res.ok) {
          this.amountError = (json.error && json.error.message) || json.message || "Could not add";
          return;
        }
        this.addServiceLine(json.line || json.data);
        this.cancelStep();
      } finally {
        this.busy = false;
      }
    },
    cancelStep() {
      this.menuMode = "cards";
      this.amountNode = null;
      this.detailHtml = "";
    },
    backGroup() {
      this.menuMode = "cards";
      this.groupStack.pop();
      const top = this.groupStack[this.groupStack.length - 1];
      if (top && top.url) return this.fetchNodes(top.url);
      if (top) return this.openGroup(top.id, true);
      return this.loadGroupsTop();
    },
    async loadCustomers() {
      try {
        const json = await apiFetch("GET", "/api/customers");
        this.customers = json.data || [];
      } catch (_) {
        this.customers = [];
      }
    },
    // Customer chooser (searchable, replaces the <select>): the visible label of
    // the selected customer, the search-filtered list, and the pick action.
    customerLabel() {
      const c = this.customers.find((x) => String(x.id) === String(this.customerId));
      return c ? c.name : "";
    },
    filteredCustomers() {
      const q = this.custSearch.trim().toLowerCase();
      const list = q
        ? this.customers.filter(
            (c) => c.name.toLowerCase().includes(q) || (c.phone || "").includes(q),
          )
        : this.customers;
      return list.slice(0, 50);
    },
    pickCustomer(c) {
      this.customerId = c ? String(c.id) : "";
      this.custOpen = false;
      this.custSearch = "";
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
    // afterDrawerMove applies the print policy after a till cash move, given the
    // API response data (which carries receipt_id for the CR- slip). With
    // askToPrint off it auto-prints; on, it opens the shared Print / Skip prompt.
    afterDrawerMove(data) {
      const id = data && data.receipt_id;
      if (!id) return;
      if (this.askToPrint) {
        window.dispatchEvent(
          new CustomEvent("app-print-prompt", {
            detail: { url: "/cashier/money-receipts/" + id + "/print" },
          })
        );
      } else {
        printMoneySlip(id);
      }
    },
    async openRegister() {
      const json = await apiFetch("POST", "/api/cash-register/open", {
        breakdown: this.buildBreakdown(this.openCounts),
        source_locker_id: Number(this.openLockerId) || 0,
      });
      this.session = json.data;
      this.openCounts = {};
      this.openLockerId = "";
      await this.loadSummary();
      await this.loadLockers();
      await this.saveDrawerSections("open");
      // Let plugin quick-action panels (e.g. Reload) that need an open drawer
      // load their session-scoped data now, in case they initialised first.
      window.dispatchEvent(new CustomEvent("register-opened"));
      toast("Register opened", "success");
      this.afterDrawerMove(json.data);
    },
    startClose() {
      this.closeCounts = {};
      this.closeResult = null;
      this.showClose = true;
      this.loadDrawerSections("close");
    },
    async submitClose() {
      if (!(await this.saveDrawerSections("close"))) return;
      const json = await apiFetch("POST", "/api/cash-register/close", {
        breakdown: this.buildBreakdown(this.closeCounts),
        dest_locker_id: Number(this.closeLockerId) || 0,
      });
      this.closeResult = json.data;
      this.session = null;
      this.closeLockerId = "";
      await this.loadSummary();
      await this.loadLockers();
      // The drawer is closed again: repopulate the Open dialog's plugin sections
      // (e.g. reload-float opening) so reopening a session on this same page still
      // prompts for them — init() only ran on first load.
      await this.loadDrawerSections("open");
      this.afterDrawerMove(json.data);
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
      const json = await apiFetch("POST", "/api/cash-register/withdraw", {
        amount: String(this.withdrawAmount),
        reason: this.withdrawReason,
        counter_locker_id: Number(this.withdrawLockerId) || 0,
      });
      this.showWithdraw = false;
      this.withdrawAmount = 0;
      this.withdrawReason = "";
      this.withdrawLockerId = "";
      await this.loadSummary();
      await this.loadLockers();
      toast("Cash withdrawn", "success");
      this.afterDrawerMove(json.data);
    },
    async deposit() {
      if (Number(this.depositAmount) <= 0) {
        toast("Enter an amount to deposit", "error");
        return;
      }
      const json = await apiFetch("POST", "/api/cash-register/pay-in", {
        amount: String(this.depositAmount),
        reason: this.depositReason,
        counter_locker_id: Number(this.depositLockerId) || 0,
      });
      this.showDeposit = false;
      this.depositAmount = 0;
      this.depositReason = "";
      this.depositLockerId = "";
      await this.loadSummary();
      await this.loadLockers();
      toast("Cash deposited", "success");
      this.afterDrawerMove(json.data);
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
      this.payments = [{ method: "", amount: 0, reference: "", deviceId: "" }];
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
    // addServiceLine adds a non-stocked service line to the cart (e.g. a plugin
    // recharge top-up). price is the per-line amount sent to the server as
    // price_override; the server honours it only for is_service products. Each
    // call is its own line (no qty merge) so several service sales can coexist.
    addServiceLine(detail) {
      detail = detail || {};
      const amt = Number(detail.price) || 0;
      this.cart.push({
        id: detail.id,
        name: detail.name,
        unit_price: amt,
        tax_rate: 0,
        qty: 1,
        stock: Number.MAX_SAFE_INTEGER,
        allowDecimal: false,
        discount: 0,
        discountType: "fixed",
        track_serial: false,
        warranty_months: 0,
        serials: [],
        is_service: true,
        price_override: String(amt),
        // Optional per-line label shown on the receipt (e.g. "A4 colour x20").
        description: detail.description || "",
        // Stock this service line consumes (e.g. paper for a document job):
        // [{product_id, quantity}]. The server depletes these FEFO and sets COGS.
        components: Array.isArray(detail.components) ? detail.components : [],
        // Recharge plugin: the device whose float this reload draws down. Recorded
        // in the device ledger after checkout via /cashier/recharge/reload.
        recharge_device_id: Number(detail.deviceId) || 0,
        // Documents plugin: job metadata recorded after checkout via
        // /cashier/documents/record (service, labour worker/amount).
        doc_job: detail.docJob || null,
      });
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
      // Quick-Sell rush handoff from the phone app: a "KQ<barcode>X<qty>" code
      // carries one product AND its quantity. Detect it before a normal lookup.
      if (/^KQ\d+X\d+$/i.test(code)) {
        return this.scanQuickSale(code);
      }
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
    // scanQuickSale handles a phone Quick-Sell barcode "KQ<barcode>X<qty>":
    // resolve the product by its real (numeric) barcode and SET that cart line's
    // quantity to <qty>. Idempotent by design — re-scanning the same code re-sets
    // the qty rather than adding again, so an accidental double-scan can't double
    // the sale.
    async scanQuickSale(code) {
      this.scan = "";
      const m = /^KQ(\d+)X(\d+)$/i.exec(code);
      if (!m) {
        toast("Unreadable quick-sale code", "error");
        return;
      }
      const barcode = m[1];
      const qty = Number(m[2]) || 0;
      if (qty <= 0) {
        toast("Quick-sale code has no quantity", "error");
        return;
      }
      try {
        const json = await apiFetch("GET", `/api/products/barcode/${encodeURIComponent(barcode)}`, undefined, { silent: true });
        const p = json.data;
        let line = this.cart.find((x) => x.id === p.id);
        if (!line) {
          this.addToCart(p);
          line = this.cart.find((x) => x.id === p.id);
        }
        if (line) {
          line.qty = line.allowDecimal ? qty : Math.round(qty);
          this.syncSerials(line);
        }
        toast(`${p.name} × ${qty}`, "success");
      } catch (e) {
        // No such product: the phone item isn't in this POS catalog — tell the
        // cashier to key it in rather than silently dropping it.
        if (e && e.status === 404) {
          toast(`No item with barcode ${barcode} — add it manually`, "error");
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
    // Pick a payment method, then jump focus straight to the amount input so the
    // cashier can type the amount without a second click. (No method is pre-selected,
    // so the cashier always chooses one deliberately.)
    selectMethod(p, method, ev) {
      p.method = method;
      if (method === "wallet") this.loadWalletDevices();
      const btnGroup = ev && ev.currentTarget && ev.currentTarget.parentElement;
      const row = btnGroup && btnGroup.parentElement;
      const amt = row && row.querySelector('input[type="number"]');
      if (amt) this.$nextTick(() => amt.focus());
    },
    // Tab from the amount input goes to this line's detail field — the card/online
    // reference input or the reload plugin's wallet device combo — skipping the
    // fill-remaining button, so the cashier reaches it without another click.
    focusDetail(ev) {
      const row = ev && ev.currentTarget && ev.currentTarget.closest(".flex");
      if (!row) return;
      const detail = [...row.querySelectorAll("[data-tender-detail]")].find((el) => el.offsetParent !== null);
      if (detail) {
        ev.preventDefault();
        detail.focus();
      }
    },
    addPayment() {
      this.payments.push({ method: "", amount: 0, reference: "", deviceId: "" });
    },
    removePayment(idx) {
      this.payments.splice(idx, 1);
      if (this.payments.length === 0) {
        this.payments.push({ method: "", amount: 0, reference: "", deviceId: "" });
      }
    },
    // Lazily load the devices a wallet (eZ Cash / mCash) payment can credit, each
    // with its live float balance. Flat list across all carriers (carrier_id=0).
    // No-op (empty list) when the recharge plugin isn't installed.
    async loadWalletDevices() {
      if (this.walletDevices.length) return;
      try {
        const json = await apiFetch("GET", "/cashier/recharge/devices?carrier_id=0&for=money", undefined, { silent: true });
        // Bank cards hold no float — a wallet transfer can only credit a tracked float.
        this.walletDevices = (json.data || []).filter((d) => d.tracks_float !== false);
      } catch (_) {
        /* recharge plugin not installed; leave list empty */
      }
    },
    // After a sale, credit each wallet tender to its device's float (recharge
    // plugin). Best-effort: the per-device closing count is authoritative anyway.
    async attributeWallet(saleId) {
      const ws = this.payments.filter(
        (p) => p.method === "wallet" && Number(p.amount) > 0 && p.deviceId
      );
      for (const w of ws) {
        try {
          await apiFetch(
            "POST",
            "/cashier/recharge/wallet",
            { sale_id: saleId, device_id: Number(w.deviceId), amount: String(w.amount) },
            { silent: true }
          );
        } catch (_) {
          /* leave float to be reconciled by the closing count */
        }
      }
    },
    // reloadFloatOK re-fetches fresh device balances and blocks checkout if the
    // reload lines in the cart would push any device's float below zero. Reload's
    // ledger decrease only lands after the sale, so this client guard is the till.
    async reloadFloatOK() {
      const loads = this.cart.filter((it) => Number(it.recharge_device_id) > 0);
      if (!loads.length) return true;
      // Sum pending reload amount per device.
      const want = {};
      for (const it of loads) {
        const d = Number(it.recharge_device_id);
        want[d] = (want[d] || 0) + this.lineNet(it);
      }
      let bal = {};
      try {
        const json = await apiFetch("GET", "/cashier/recharge/devices?carrier_id=0&for=recharge", undefined, { silent: true });
        for (const d of json.data || []) bal[d.id] = Number(d.balance);
      } catch (_) {
        toast("Couldn't verify recharge float — try again", "error");
        return false;
      }
      for (const id of Object.keys(want)) {
        if (want[id] > (bal[id] || 0)) {
          const dev = (this.walletDevices.find((x) => String(x.id) === id) || {});
          toast(`Not enough float on ${dev.label || "device #" + id} for this reload`, "error");
          return false;
        }
      }
      return true;
    },
    // After a sale, record each reload line in the device ledger (float −). The
    // amount was already collected by the core sale's payment, so this row is
    // cash-neutral. Best-effort, mirroring attributeWallet.
    async attributeReloads(saleId) {
      const loads = this.cart.filter((it) => Number(it.recharge_device_id) > 0);
      for (const it of loads) {
        try {
          await apiFetch(
            "POST",
            "/cashier/recharge/reload",
            { sale_id: saleId, device_id: Number(it.recharge_device_id), amount: String(this.lineNet(it)) },
            { silent: true }
          );
        } catch (_) {
          /* leave float to be reconciled by the closing count */
        }
      }
    },
    // After a sale, record each document-job cart line (sale_id + job metadata) so
    // the documents plugin writes its doc_job ledger (analytics + worker labour).
    // Paper/film stock was already depleted in the sale tx via the consume-on-sale
    // seam; this call is purely the plugin's bookkeeping. Best-effort.
    async recordDocJobs(saleId) {
      const jobs = this.cart.filter((it) => it.doc_job).map((it) => it.doc_job);
      if (!jobs.length) return;
      try {
        await apiFetch(
          "POST",
          "/cashier/documents/record",
          { sale_id: saleId, jobs },
          { silent: true }
        );
      } catch (_) {
        /* analytics-only; the sale itself already succeeded */
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
      // Every tendered amount must have a method chosen (nothing defaults to cash).
      for (const p of this.payments) {
        if (Number(p.amount) > 0 && !p.method) {
          toast("Choose a payment method for each amount", "error");
          return;
        }
      }
      // A wallet (eZ Cash / mCash) tender must name the device it credits.
      for (const p of this.payments) {
        if (p.method === "wallet" && Number(p.amount) > 0 && !p.deviceId) {
          toast("Choose a device for the wallet payment", "error");
          return;
        }
      }
      // Hard-block: reload lines draw a device's float down. Re-check each device's
      // fresh balance against the sum of pending reloads for it before selling,
      // since the float decrease is attributed only after the sale commits.
      if (!(await this.reloadFloatOK())) return;
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
            // Service lines (plugin recharge) carry a per-line amount; ignored by
            // the server for normal stocked products.
            price_override: it.price_override || "",
            // Optional per-line label + consumables for service lines.
            description: it.description || "",
            components: Array.isArray(it.components) ? it.components : [],
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
        await this.attributeWallet(json.data.sale.id);
        await this.attributeReloads(json.data.sale.id);
        await this.recordDocJobs(json.data.sale.id);
        toast("Sale complete", "success");
        if (this.askToPrint) {
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
      this.payments = [{ method: "", amount: 0, reference: "", deviceId: "" }];
      this.customerId = "";
      this.receipt = null;
    },
  };
}

// grn: the Purchase Order entry screen. Creates (or edits) a *draft* — no stock
// effects until the order is received. `config` is null for a new order, or
// {editId, supplierId, notes, lines} to edit an existing draft. Line math here is
// a preview only — the server recomputes totals authoritatively.
// poProductSearch / poProductChoose back the live product search picker shared by
// the purchase-order entry form and the receive screen. They mutate a line object
// in place: set its search results, or fill product_id/name + cost/sell on pick.
async function poProductSearch(line) {
  const q = (line.product_name || "").trim();
  if (!q) {
    line._results = [];
    line._open = false;
    return;
  }
  try {
    const json = await apiFetch("GET", "/api/products?limit=20&search=" + encodeURIComponent(q));
    line._results = json.data || [];
    line._open = true;
  } catch (_) {
    line._results = [];
  }
}
function poProductChoose(line, r) {
  line.product_id = r.id;
  line.product_name = r.name;
  line.cost_price = Number(r.cost_price) || 0;
  line.selling_price = Number(r.selling_price) || 0;
  line.cur_cost = Number(r.cost_price) || 0;
  line.cur_sell = Number(r.selling_price) || 0;
  line._results = [];
  line._open = false;
}

// docConsumablePicker backs the live search-box for the documents consumable
// form: type to find a paper/film stock product (service products are excluded
// server-side) and pick it to fill the hidden product_id the form submits.
function docConsumablePicker() {
  return {
    productId: "",
    name: "",
    open: false,
    results: [],
    async search() {
      const q = (this.name || "").trim();
      if (!q) {
        this.results = [];
        this.open = false;
        return;
      }
      try {
        const json = await apiFetch("GET", "/api/products?limit=20&search=" + encodeURIComponent(q));
        this.results = json.data || [];
        this.open = true;
      } catch (_) {
        this.results = [];
      }
    },
    choose(r) {
      this.productId = String(r.id);
      this.name = r.name;
      this.open = false;
    },
  };
}

// entityPicker is the generic searchable single-select used to replace plain
// <select> dropdowns that can grow large (products, suppliers, customers, …).
// cfg: { id, name, url, minLen, param }. The search endpoint must accept a
// ?<param>= query (default "search") and return { data: [{id, name, ...}] }.
// The picker only tracks id/name; the template's x-for renders whatever result
// fields it wants (e.g. on-hand for products). Used via the ProductPicker /
// EntityPicker templ components.
function entityPicker(cfg) {
  cfg = cfg || {};
  return {
    id: cfg.id ? String(cfg.id) : "",
    name: cfg.name || "",
    open: false,
    results: [],
    url: cfg.url || "/api/products",
    param: cfg.param || "search",
    minLen: cfg.minLen || 1,
    async search() {
      const q = (this.name || "").trim();
      if (q.length < this.minLen) {
        this.results = [];
        this.open = false;
        return;
      }
      try {
        const sep = this.url.indexOf("?") === -1 ? "?" : "&";
        const json = await apiFetch(
          "GET",
          this.url + sep + this.param + "=" + encodeURIComponent(q) + "&limit=20",
        );
        this.results = json.data || [];
        this.open = true;
      } catch (_) {
        this.results = [];
      }
    },
    choose(r) {
      this.id = String(r.id);
      this.name = r.name;
      this.open = false;
      // Let a parent Alpine component sync its own state (e.g. an x-model the
      // picker replaced). field disambiguates multiple pickers in one form.
      this.$dispatch("picked", { field: this.field, id: this.id, name: this.name, item: r });
    },
    clear() {
      this.id = "";
      this.name = "";
      this.results = [];
      this.open = false;
      this.$dispatch("picked", { field: this.field, id: "", name: "", item: null });
    },
    field: cfg.field || "",
  };
}

// optionPicker is the client-side searchable single-select for small/medium
// fixed lists passed from the server (units, a category tree, …) — no API call.
// cfg: { id, options:[{id,label}], placeholder }. Submits the chosen id via a
// hidden input bound to `id`. Backed by the OptionPicker templ component.
function optionPicker(cfg) {
  cfg = cfg || {};
  return {
    open: false,
    query: "",
    id: cfg.id ? String(cfg.id) : "",
    options: cfg.options || [],
    placeholder: cfg.placeholder || "Select…",
    filtered() {
      const q = this.query.trim().toLowerCase();
      if (!q) return this.options;
      return this.options.filter((o) => o.label.toLowerCase().includes(q));
    },
    label() {
      const o = this.options.find((o) => String(o.id) === String(this.id));
      return o ? o.label : "";
    },
    toggle() {
      this.open = !this.open;
      if (this.open) this.$nextTick(() => this.$refs.q && this.$refs.q.focus());
    },
    pick(o) {
      this.id = o ? String(o.id) : "";
      this.open = false;
      this.query = "";
    },
  };
}

// reportProductPicker backs the search-box product selector on the per-product
// report. Choosing a result fills the hidden `product` field and submits the
// range form so the chart reloads for that product (keeping the date range/group).
function reportProductPicker(initialId, initialName) {
  return {
    productId: initialId || "",
    name: initialName || "",
    open: false,
    results: [],
    async search() {
      const q = (this.name || "").trim();
      if (!q) {
        this.results = [];
        this.open = false;
        return;
      }
      try {
        const json = await apiFetch("GET", "/api/products?limit=20&search=" + encodeURIComponent(q));
        this.results = json.data || [];
        this.open = true;
      } catch (_) {
        this.results = [];
      }
    },
    choose(r) {
      this.productId = String(r.id);
      this.name = r.name;
      this.open = false;
      this.$nextTick(() => this.$root.closest("form").submit());
    },
  };
}

function grn(symbol, config) {
  config = config || {};
  return {
    sym: symbol,
    editId: Number(config.editId) || 0,
    supplierId: config.supplierId || "",
    supplierName: config.supplierName || "", // searchable supplier chooser
    supOpen: false,
    supQuery: "",
    supResults: [],
    expectedDate: config.expectedDate || "",
    notes: config.notes || "",
    lines: [],
    busy: false,

    pick(l) {
      poProductSearch(l);
    },
    choose(l, r) {
      poProductChoose(l, r);
      // Default the supplier to the product's preferred supplier (if none chosen yet).
      if (!this.supplierId && r.preferred_supplier_id) {
        this.supplierId = String(r.preferred_supplier_id);
        this.supplierName = r.preferred_supplier_name || "";
      }
    },
    async searchSuppliers() {
      const q = this.supQuery.trim();
      if (!q) { this.supResults = []; return; }
      try {
        const json = await apiFetch("GET", "/api/suppliers?search=" + encodeURIComponent(q) + "&limit=20", undefined, { silent: true });
        this.supResults = json.data || [];
      } catch (_) { this.supResults = []; }
    },
    pickSupplier(s) {
      this.supplierId = s ? String(s.id) : "";
      this.supplierName = s ? s.name : "";
      this.supOpen = false;
      this.supQuery = "";
    },

    init() {
      if (Array.isArray(config.lines) && config.lines.length > 0) {
        this.lines = config.lines.map((l) => ({
          product_id: Number(l.product_id) || 0,
          product_name: l.name || "",
          quantity: Number(l.quantity) || 0,
          cost_price: Number(l.cost_price) || 0,
          selling_price: Number(l.selling_price) || 0,
          expiry_date: l.expiry_date || "",
          _open: false,
          _results: [],
        }));
      } else {
        this.addLine();
      }
    },
    money(v) {
      const n = Number(v) || 0;
      return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    },
    addLine() {
      this.lines.push({ product_id: 0, product_name: "", quantity: 0, cost_price: 0, selling_price: 0, expiry_date: "", _open: false, _results: [] });
    },
    removeLine(i) {
      this.lines.splice(i, 1);
      if (this.lines.length === 0) this.addLine();
    },
    // F4 add line · F10 save. Touch steppers on the qty column.
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
      const url = this.editId > 0 ? "/admin/purchases/" + this.editId + "/edit" : "/admin/purchases";
      this.busy = true;
      try {
        await apiFetch("POST", url, {
          supplier_id: Number(this.supplierId),
          discount: "0",
          paid_amount: "0",
          expected_date: this.expectedDate || "",
          notes: this.notes || null,
          items: items,
        });
        toast(this.editId > 0 ? "Purchase order updated" : "Purchase order saved", "success");
        window.location = "/admin/purchases";
      } catch (_) {
        /* toast already shown */
      } finally {
        this.busy = false;
      }
    },
  };
}

// grnReceive: the receive screen for a draft. Shows ordered vs editable received
// qty (overstock allowed); posts the actually-received lines + invoice/paid.
function grnReceive(symbol, config) {
  config = config || {};
  return {
    sym: symbol,
    id: Number(config.id) || 0,
    invoiceNo: "",
    dueDate: "",
    discount: 0,
    paid: 0,
    keepRemainder: true,
    busy: false,
    lines: (config.lines || []).map((l) => ({
      product_id: Number(l.product_id) || 0,
      product_name: l.product_name || "",
      ordered: l.ordered || "0",
      quantity: Number(l.quantity) || 0,
      cost_price: Number(l.cost_price) || 0,
      selling_price: Number(l.selling_price) || 0,
      expiry_date: l.expiry_date || "",
      cur_cost: Number(l.cur_cost) || 0,
      cur_sell: Number(l.cur_sell) || 0,
      _new: false,
      _open: false,
      _results: [],
    })),

    pick(l) {
      poProductSearch(l);
    },
    choose(l, r) {
      poProductChoose(l, r);
    },
    // addNewLine lets you receive a forgotten/extra item that wasn't on the draft.
    addNewLine() {
      this.lines.push({
        product_id: 0, product_name: "", ordered: "", quantity: 1,
        cost_price: 0, selling_price: 0, expiry_date: "",
        cur_cost: 0, cur_sell: 0, _new: true, _open: false, _results: [],
      });
    },
    removeLine(i) {
      this.lines.splice(i, 1);
    },

    money(v) {
      const n = Number(v) || 0;
      return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    },
    lineSub(l) {
      return (Number(l.quantity) || 0) * (Number(l.cost_price) || 0);
    },
    subtotal() {
      return this.lines.reduce((s, l) => s + this.lineSub(l), 0);
    },
    total() {
      return Math.max(0, this.subtotal() - (Number(this.discount) || 0));
    },
    hasVariance() {
      return this.lines.some((l) => Number(l.quantity) !== Number(l.ordered));
    },
    // hasShortfall is true when any ordered line was received short (rest can be
    // kept on order as a new draft).
    hasShortfall() {
      return this.lines.some((l) => !l._new && Number(l.quantity) < Number(l.ordered));
    },
    // suggestSell proposes a selling price that keeps the product's previous
    // markup against the new received cost (falls back to a 20% markup).
    suggestSell(l) {
      const newCost = Number(l.cost_price) || 0;
      const curCost = Number(l.cur_cost) || 0;
      const curSell = Number(l.cur_sell) || 0;
      const base = curCost > 0 && curSell > 0 ? newCost * (curSell / curCost) : newCost * 1.2;
      return Math.round(base * 100) / 100;
    },
    // marginWarn flags a line where the received cost would sell at/below cost or
    // squeeze the margin below the previous markup.
    marginWarn(l) {
      const newCost = Number(l.cost_price) || 0;
      const sell = Number(l.selling_price) || 0;
      if (newCost <= 0) return false;
      if (sell > 0 && newCost >= sell) return true;
      return this.suggestSell(l) > sell + 0.005;
    },
    async submit() {
      if (this.busy) return;
      const items = this.lines
        .filter((l) => Number(l.product_id) > 0 && Number(l.quantity) > 0)
        .map((l) => ({
          product_id: Number(l.product_id),
          quantity: String(l.quantity),
          ordered_qty: l._new ? "" : String(l.ordered || 0),
          cost_price: String(l.cost_price || 0),
          selling_price: String(l.selling_price || 0),
          expiry_date: l.expiry_date || "",
        }));
      if (items.length === 0) {
        toast("Receive at least one line", "error");
        return;
      }
      this.busy = true;
      try {
        await apiFetch("POST", "/admin/purchases/" + this.id + "/receive", {
          invoice_no: this.invoiceNo || null,
          discount: String(this.discount || 0),
          paid_amount: String(this.paid || 0),
          due_date: this.dueDate || "",
          keep_remainder: !!this.keepRemainder && this.hasShortfall(),
          items: items,
        });
        toast("Goods received", "success");
        window.location = "/admin/purchases/" + this.id;
      } catch (_) {
        /* toast already shown */
      } finally {
        this.busy = false;
      }
    },
  };
}

// poBuilder: the low-stock reorder picker. Tick items, set qty + supplier, then
// create one draft Purchase Order per supplier and open the printable order(s).
function poBuilder(rows) {
  return {
    busy: false,
    lines: (rows || []).map((r) => ({
      product_id: Number(r.product_id) || 0,
      name: r.name || "",
      on_hand: r.on_hand || "0",
      unit: r.unit || "",
      suggested: Number(r.suggested) || 0,
      cost: r.cost || "0",
      supplier_id: Number(r.supplier_id) || 0,
      supplier_name: r.supplier_name || "", // searchable per-line supplier chooser
      _supOpen: false,
      _supQuery: "",
      _supResults: [],
      selected: false,
    })),
    async searchLineSuppliers(l) {
      const q = l._supQuery.trim();
      if (!q) { l._supResults = []; return; }
      try {
        const json = await apiFetch("GET", "/api/suppliers?search=" + encodeURIComponent(q) + "&limit=20", undefined, { silent: true });
        l._supResults = json.data || [];
      } catch (_) { l._supResults = []; }
    },
    pickLineSupplier(l, s) {
      l.supplier_id = s ? Number(s.id) : 0;
      l.supplier_name = s ? s.name : "";
      l._supOpen = false;
      l._supQuery = "";
    },

    toggleAll(on) {
      this.lines.forEach((l) => (l.selected = !!on));
    },
    selected() {
      return this.lines.filter((l) => l.selected && Number(l.suggested) > 0);
    },
    selectedCount() {
      return this.selected().length;
    },
    async build() {
      if (this.busy) return;
      const picked = this.selected();
      if (picked.length === 0) {
        toast("Tick at least one item to order", "error");
        return;
      }
      if (picked.some((l) => Number(l.supplier_id) <= 0)) {
        toast("Choose a supplier for every ticked item", "error");
        return;
      }
      const lines = picked.map((l) => ({
        product_id: Number(l.product_id),
        supplier_id: Number(l.supplier_id),
        quantity: String(l.suggested),
        cost_price: String(l.cost || 0),
      }));
      this.busy = true;
      try {
        const res = await apiFetch("POST", "/admin/purchases/draft", { lines });
        const ids = (res && res.data && res.data.ids) || [];
        toast(ids.length + " purchase order(s) created", "success");
        if (ids.length > 0) {
          window.location = "/admin/purchases/po/print?mode=combined&ids=" + ids.join(",");
        } else {
          window.location = "/admin/purchases";
        }
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
    supplierName: "", // searchable supplier chooser
    supOpen: false,
    supQuery: "",
    supResults: [],
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
    async searchSuppliers() {
      const q = this.supQuery.trim();
      if (!q) { this.supResults = []; return; }
      try {
        const json = await apiFetch("GET", "/api/suppliers?search=" + encodeURIComponent(q) + "&limit=20", undefined, { silent: true });
        this.supResults = json.data || [];
      } catch (_) { this.supResults = []; }
    },
    pickSupplier(s) {
      this.supplierId = s ? String(s.id) : "";
      this.supplierName = s ? s.name : "";
      this.supOpen = false;
      this.supQuery = "";
    },
    // Per-line product search (shared pickers).
    pick(l) {
      poProductSearch(l);
    },
    choose(l, r) {
      poProductChoose(l, r);
    },
    addLine() {
      this.lines.push({ product_id: 0, product_name: "", quantity: 0, cost_price: 0, _open: false, _results: [] });
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
    dispos: {},
    reason: "",
    busy: false,
    setQty(itemId, value, max) {
      let q = Number(value) || 0;
      if (q < 0) q = 0;
      if (q > max) q = max;
      this.qtys[itemId] = q;
    },
    setDispo(itemId, value) {
      this.dispos[itemId] = value;
    },
    async submit() {
      if (this.busy) return;
      const lines = Object.keys(this.qtys)
        .filter((k) => Number(this.qtys[k]) > 0)
        .map((k) => ({
          sale_item_id: Number(k),
          quantity: String(this.qtys[k]),
          disposition: this.dispos[k] || "restock",
        }));
      if (lines.length === 0) {
        toast("Enter at least one quantity to return", "error");
        return;
      }
      const anyDamaged = lines.some((l) => l.disposition === "damage");
      this.busy = true;
      try {
        await apiFetch("POST", endpoint, {
          reason: this.reason || null,
          lines: lines,
        });
        toast(anyDamaged ? "Return processed" : "Return processed & restocked", "success");
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
    pId: 0,
    pName: "",
    pCode: "",
    pPrice: "",
    pShowPrice: true,
    pHasBarcode: false,
    pGenBusy: false,
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
    // onProductPick is the searchable ProductPicker's equivalent of onProduct:
    // it takes the chosen product object (from the "picked" event) and rebuilds
    // the live preview. A product with no barcode leaves pCode empty (print
    // stays disabled) and reveals the "Generate barcode" action instead of
    // silently printing an unscannable SKU. The label-count field auto-fills
    // with the on-hand stock quantity (editable).
    onProductPick(item) {
      if (!item) {
        this.pId = 0;
        this.pName = "";
        this.pCode = "";
        this.pPrice = "";
        this.pHasBarcode = false;
        return;
      }
      this.pId = item.id || 0;
      this.pName = item.name || "";
      this.pHasBarcode = !!item.barcode;
      this.pCode = item.barcode || "";
      this.pPrice =
        this.sym +
        " " +
        (Number(item.selling_price) || 0).toLocaleString(undefined, {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        });
      if (this.$refs.pQty) {
        this.$refs.pQty.value = String(
          Math.max(1, Math.floor(Number(item.stock_qty) || 0)),
        );
      }
      this.$nextTick(() => this.draw("preview-product", this.pCode, "CODE128"));
    },
    // generateProductBarcode mints a fresh EAN-13 and saves it onto the picked
    // product (which had none), so the printed label actually scans at the till.
    async generateProductBarcode() {
      if (!this.pId || this.pGenBusy) return;
      this.pGenBusy = true;
      try {
        const gen = await fetch("/api/products/barcode/generate", {
          credentials: "same-origin",
        });
        const genJson = await gen.json();
        if (!gen.ok || !genJson.success) {
          throw new Error(genJson?.error?.message || "Could not generate a barcode");
        }
        const code = genJson.data.barcode;
        const body = new URLSearchParams({ barcode: code });
        const save = await fetch("/api/products/" + this.pId + "/barcode", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: body,
        });
        const saveJson = await save.json();
        if (!save.ok || !saveJson.success) {
          throw new Error(saveJson?.error?.message || "Could not save the barcode");
        }
        this.pCode = code;
        this.pHasBarcode = true;
        this.$nextTick(() => this.draw("preview-product", this.pCode, "CODE128"));
        toast("Barcode " + code + " saved to " + this.pName, "success");
      } catch (e) {
        toast(e.message || "Could not generate a barcode", "error");
      } finally {
        this.pGenBusy = false;
      }
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

// intake powers the Stock Intake page: one search box drives two modes — pick an
// existing product to restock/reprint, or type a new name to create it — then set
// quantity and (optionally) print labels, accumulating an "added this session"
// list. Saves post form-encoded so the Go handlers can read form values; printing
// reuses the existing /admin/labels/send endpoint.
function intake(sym) {
  return {
    sym: sym,
    mode: "", // "" | "new" | "restock"
    q: "",
    results: [],
    open: false,
    // restock target
    pId: 0,
    pName: "",
    pBarcode: "",
    pHasBarcode: false,
    pStock: 0,
    pCost: "",
    pSelling: "",
    pWholesale: "",
    pGenBusy: false,
    // create
    newName: "",
    cBarcode: "",
    // shared form state
    qty: "",
    labelQty: "1",
    printLabels: true,
    showPrice: true,
    // session list
    items: [],
    seq: 0,
    busy: false,

    // syncLabelQty mirrors the stock quantity into the label count, which is
    // almost always what you want: 12 pens in, 12 stickers out.
    //
    // It must clamp. The label input is a whole number from 1 to 200, so a
    // quantity of 0, 2.5 or 250 copied across verbatim makes it invalid, and an
    // invalid field blocks the whole form — silently, because when "Print
    // labels" is off the field is hidden and the browser cannot focus it to
    // show the message. Stock simply never got created.
    syncLabelQty() {
      const n = Math.floor(Number(this.qty));
      if (!Number.isFinite(n) || n < 1) {
        this.labelQty = "1";
        return;
      }
      this.labelQty = String(Math.min(200, n));
    },

    async search() {
      const s = (this.q || "").trim();
      if (!s) {
        this.results = [];
        return;
      }
      try {
        const json = await apiFetch(
          "GET",
          "/api/products?search=" + encodeURIComponent(s) + "&limit=20",
        );
        this.results = json.data || [];
        this.open = true;
      } catch (_) {
        this.results = [];
      }
    },
    pickExisting(r) {
      this.mode = "restock";
      this.pId = r.id;
      this.pName = r.name;
      this.pBarcode = r.barcode || "";
      this.pHasBarcode = !!r.barcode;
      this.pStock = Number(r.stock_qty) || 0;
      this.pCost = r.cost_price != null ? String(r.cost_price) : "";
      this.pSelling = r.selling_price != null ? String(r.selling_price) : "";
      this.pWholesale = r.wholesale_price != null ? String(r.wholesale_price) : "";
      this.q = r.name;
      this.qty = "";
      this.labelQty = "1";
      this.open = false;
      this.drawRestock();
    },
    createNew() {
      this.mode = "new";
      this.newName = (this.q || "").trim();
      this.cBarcode = "";
      this.qty = "";
      this.labelQty = "1";
      this.open = false;
      this.drawNew();
    },
    reset() {
      this.mode = "";
      this.q = "";
      this.results = [];
      this.open = false;
      this.pId = 0;
      this.pName = "";
      this.pBarcode = "";
      this.pHasBarcode = false;
      this.pStock = 0;
      this.pCost = "";
      this.pSelling = "";
      this.pWholesale = "";
      this.newName = "";
      this.cBarcode = "";
      this.qty = "";
      this.labelQty = "1";
      this.$nextTick(() => this.$refs.searchInput && this.$refs.searchInput.focus());
    },
    // --- live barcode preview (JsBarcode) ---
    drawInto(id, code) {
      const el = document.getElementById(id);
      if (!el) return;
      if (!code) {
        el.innerHTML = "";
        return;
      }
      try {
        JsBarcode(el, code, {
          format: "CODE128",
          displayValue: true,
          fontSize: 12,
          height: 38,
          margin: 2,
          width: 1.4,
        });
      } catch (_) {
        el.innerHTML = "";
      }
    },
    drawNew() {
      this.$nextTick(() => this.drawInto("intake-preview-new", this.cBarcode));
    },
    drawRestock() {
      this.$nextTick(() => this.drawInto("intake-preview-restock", this.pBarcode));
    },
    // genBarcode mints + saves an EAN-13 onto the picked barcode-less product.
    async genBarcode() {
      if (!this.pId || this.pGenBusy) return;
      this.pGenBusy = true;
      try {
        const gen = await fetch("/api/products/barcode/generate", {
          credentials: "same-origin",
        });
        const gj = await gen.json();
        if (!gen.ok || !gj.success) {
          throw new Error(gj?.error?.message || "Could not generate a barcode");
        }
        const code = gj.data.barcode;
        const save = await fetch("/api/products/" + this.pId + "/barcode", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({ barcode: code }),
        });
        const sj = await save.json();
        if (!save.ok || !sj.success) {
          throw new Error(sj?.error?.message || "Could not save the barcode");
        }
        this.pBarcode = code;
        this.pHasBarcode = true;
        this.drawRestock();
        toast("Barcode " + code + " saved", "success");
      } catch (e) {
        toast(e.message || "Could not generate a barcode", "error");
      } finally {
        this.pGenBusy = false;
      }
    },
    async postForm(url, form) {
      const res = await fetch(url, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams(new FormData(form)),
      });
      const json = await res.json();
      if (!res.ok || !json.success) {
        throw new Error(json?.error?.message || "Request failed");
      }
      return json.data;
    },
    async createItem(form) {
      if (this.busy) return;
      this.busy = true;
      try {
        const item = await this.postForm("/admin/inventory/intake/create", form);
        await this.afterSave(item, form);
      } catch (e) {
        toast(e.message || "Could not save the item", "error");
      } finally {
        this.busy = false;
      }
    },
    async restockItem(form) {
      if (this.busy) return;
      this.busy = true;
      try {
        const item = await this.postForm("/admin/inventory/intake/restock", form);
        await this.afterSave(item, form);
      } catch (e) {
        toast(e.message || "Could not add stock", "error");
      } finally {
        this.busy = false;
      }
    },
    async afterSave(item, form) {
      if (this.printLabels && item.barcode) {
        try {
          await this.sendLabels(item.id, new FormData(form));
        } catch (_) {
          toast("Saved, but printing failed — use Reprint", "error");
        }
      } else if (this.printLabels && !item.barcode) {
        toast("Saved. Add a barcode to print a label.", "info");
      } else {
        toast("Saved", "success");
      }
      item.key = ++this.seq;
      this.items.unshift(item);
      this.reset();
    },
    async sendLabels(productId, fd) {
      const p = new URLSearchParams();
      p.set("product_id", String(productId));
      p.set("qty", fd.get("label_qty") || "1");
      if (fd.get("show_price")) p.set("show_price", "1");
      p.set("label_size", fd.get("label_size") || "default");
      if (fd.get("label_w")) p.set("label_w", fd.get("label_w"));
      if (fd.get("label_h")) p.set("label_h", fd.get("label_h"));
      if (fd.get("label_gap")) p.set("label_gap", fd.get("label_gap"));
      const res = await fetch("/admin/labels/send", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: p,
      });
      if (!res.ok) throw new Error("print failed");
    },
    async reprint(it) {
      try {
        const p = new URLSearchParams({
          product_id: String(it.id),
          qty: String(Math.max(1, Math.floor(Number(it.qty)) || 1)),
          label_size: "default",
        });
        const res = await fetch("/admin/labels/send", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: p,
        });
        if (!res.ok) throw new Error("print failed");
        toast("Reprinted " + it.name, "success");
      } catch (_) {
        toast("Could not reprint", "error");
      }
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
      else this.cancelCreate();
    },
    pick(o) {
      this.selected = o ? String(o.id) : "";
      this.open = false;
      this.query = "";
      this.cancelCreate();
      if (this.reload) this.$nextTick(() => this.$dispatch("category-changed"));
    },
    clear() {
      this.pick(null);
    },

    // --- inline category creation (only rendered when allowCreate) ---
    allowCreate: !!cfg.allowCreate,
    creating: false,
    createParent: null,
    newName: "",
    createError: "",
    createBusy: false,

    startCreate(parent) {
      this.creating = true;
      this.createParent = parent;
      this.newName = this.query || "";
      this.createError = "";
      this.$nextTick(() => this.$refs.newName && this.$refs.newName.focus());
    },
    cancelCreate() {
      this.creating = false;
      this.createParent = null;
      this.newName = "";
      this.createError = "";
    },
    async submitCreate() {
      if (this.createBusy) return;
      const name = (this.newName || "").trim();
      if (!name) {
        this.createError = "Enter a category name.";
        return;
      }
      this.createBusy = true;
      this.createError = "";
      try {
        const body = new URLSearchParams({ name: name });
        if (this.createParent) body.set("parent_id", String(this.createParent.id));
        const res = await fetch("/admin/categories/quick", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
          body: body,
        });
        const json = await res.json().catch(() => ({}));
        if (!res.ok) {
          this.createError = (json.error && json.error.message) || "Could not create that category.";
          return;
        }
        const created = json.data || json;
        // Splice the new option in directly after its parent so the indented
        // list still reads as a tree; append when it is top-level.
        const opt = { id: created.id, name: created.name, depth: created.depth || 0 };
        const at = this.createParent
          ? this.options.findIndex((o) => String(o.id) === String(this.createParent.id))
          : -1;
        const existing = this.options.findIndex((o) => String(o.id) === String(opt.id));
        if (existing >= 0) {
          this.options.splice(existing, 1, opt);
        } else if (at >= 0) {
          this.options.splice(at + 1, 0, opt);
        } else {
          this.options.push(opt);
        }
        this.selected = String(opt.id);
        this.query = "";
        this.cancelCreate();
        this.open = false;
      } finally {
        this.createBusy = false;
      }
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

// recipeEditor backs the admin recipe modal. Rows already stored are rendered
// server-side; ingredients picked in this session live in `added` until save.
//
// The picker itself is a separate Alpine scope, so we read its chosen id/name
// through Alpine.$data rather than the DOM — setting the hidden input directly
// would be overwritten by its own x-bind on the next tick.
function recipeEditor() {
  return {
    added: [],
    // Extra blank cost-line rows the user asked for. One blank row is always
    // rendered; this holds any beyond it.
    extraCosts: [],
    error: "",
    addFromPicker(root) {
      this.error = "";
      const scope = root.querySelector("[x-data]");
      const picker = scope && window.Alpine ? window.Alpine.$data(scope) : null;
      if (!picker) return;
      const id = Number(picker.id || 0);
      const name = picker.name || "";
      if (!id) {
        this.error = "Pick an ingredient first.";
        return;
      }
      // One row per ingredient: a duplicate would be rejected by
      // product_recipes_unique_component, and saying so here explains why.
      const present = this.$root.querySelectorAll('input[name="component_id[]"]');
      for (const el of present) {
        if (Number(el.value) === id) {
          this.error = name + " is already an ingredient.";
          return;
        }
      }
      const chosen = (picker.results || []).find((r) => Number(r.id) === id);
      this.added.push({ id: id, name: name, unit: chosen ? chosen.unit_abbr : "" });
      picker.id = "";
      picker.name = "";
      picker.results = [];
      picker.open = false;
    },
  };
}
