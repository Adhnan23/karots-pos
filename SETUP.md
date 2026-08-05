<div align="center">

# 🛠️ Karots POS — Setup Guide

![Time](https://img.shields.io/badge/setup-~30%20minutes-16A34A?style=for-the-badge)
![Difficulty](https://img.shields.io/badge/difficulty-beginner%20friendly-22C55E?style=for-the-badge)
![OS](https://img.shields.io/badge/OS-Linux%20(recommended)%20%7C%20Windows-4B5563?style=for-the-badge)
![Database](https://img.shields.io/badge/needs-PostgreSQL-4169E1?style=flat-square&logo=postgresql&logoColor=white)

</div>

> 📖 **In-depth printing reference** (paper widths, labels, cash drawer, logos,
> troubleshooting) lives in **[`PRINTING.md`](PRINTING.md)**. Developer/build docs
> are in **[`README.md`](README.md)**.

This guide takes you from a freshly downloaded program to a running point-of-sale
system that prints receipts and backs itself up. You only need two things from us:

1. **The program** — a single file (`karots-pos` on Linux, `karots-pos.exe` on Windows).
2. **This guide.**

You provide one thing yourself: a **PostgreSQL database** (instructions below). If
you get stuck setting up the database, contact us and we'll do it for you.

> **Linux is the recommended platform** (Ubuntu, Fedora, or Arch). The app also
> runs on Windows, but **receipt/label printing only works on Linux** — see the
> Windows section for why.

---

## How it works (30-second version)

- The program is **one self-contained file**. Everything (web pages, database
  schema) is built in. There is nothing to "install" beyond placing the file.
- It needs a **database** to store your data, and a few **settings** (called
  *environment variables*) — most importantly the database address and a secret key.
- It **creates and updates its own database tables automatically** every time it
  starts. You never run database scripts by hand.
- It serves a website on your machine (default `http://localhost:3000`) that you
  open in a browser to use the till.

> ⚠️ **Important:** the program reads its settings from **environment variables**,
> *not* automatically from a `.env` file. The recommended Linux setup below (a
> systemd service) loads your `.env` for you. If you ever run the program by
> double-clicking or typing its name directly, the settings won't be picked up
> unless you've exported them first.

---

## 0. Automated install (Linux — recommended)

On a **fresh Linux PC** you don't have to do the steps below by hand. Put the
program (`karots-pos`) and **`install.sh`** next to each other (e.g. in
`~/Downloads`) and run:

```bash
sudo ./install.sh
```

It works on **Debian/Ubuntu/Xubuntu/Mint, Fedora, Arch/CachyOS and openSUSE**, and
is safe to re-run. In one go it: installs PostgreSQL + CUPS + Chromium, creates the
`pos_db` database, moves the binary to `/opt/karots-pos`, writes a production `.env`
(with a generated DB password, JWT secret and a `backups/` folder), runs the
one-time `-init`, installs a **systemd service** (auto-starts on boot), and sets up
a **Chromium kiosk** that opens the till full-screen at login. Nothing else to
install — no Go, no Node.

After it finishes, jump to **first login** in §3, and set up your printer in §6
(a raw CUPS queue). The rest of this guide is the manual equivalent, per step.

> The binary must be a **static** build (`make build` — `CGO_ENABLED=0 GOAMD64=v1`)
> so it runs on any distro and any x86-64 CPU. `install.sh` warns if you hand it a
> dynamically-linked one. Verify your build with `make verify-portable`.

---

## 1. Settings (environment variables)

These control how the program runs. Required ones must be set or the program won't
start.

| Variable | Required? | Default | What it is |
|---|---|---|---|
| `DATABASE_URL` | **Yes** | — | Address of your PostgreSQL database (see §2). |
| `JWT_SECRET` | **Yes** | — | A random secret for securing logins. **Minimum 32 characters.** |
| `SERVER_PORT` | No | `3000` | The port the website runs on. |
| `APP_ENV` | No | `development` | Set to `production` on a real shop machine (enables secure cookies). |
| `JWT_EXPIRES_IN` | No | `15m` | How long a login lasts before re-login. `12h` is convenient for a shift. |
| `JWT_REFRESH_EXPIRES_IN` | No | `168h` | Refresh-token lifetime (7 days). |
| `CORS_ORIGINS` | No | `http://localhost:3000` | Only matters if you access the API from another site. |
| `BACKUP_DIR` | No | *(empty = off)* | Folder for automatic backups. **Set this to enable auto-backup** (see §7). |
| `BACKUP_INTERVAL` | No | `6h` | How often to auto-backup (e.g. `6h`, `1h`). |
| `BACKUP_KEEP` | No | `28` | How many backup files to keep before deleting the oldest. |

### Your settings file (`.env`)

Create a file named `.env` next to the program. Example for a real shop:

```ini
APP_ENV=production
DATABASE_URL=postgres://pos_user:CHANGE_THIS_PASSWORD@localhost:5432/pos_db?sslmode=disable
SERVER_PORT=3000
JWT_SECRET=PASTE_A_LONG_RANDOM_STRING_HERE
JWT_EXPIRES_IN=12h
JWT_REFRESH_EXPIRES_IN=168h
BACKUP_DIR=/var/lib/karots-pos/backups
BACKUP_INTERVAL=6h
BACKUP_KEEP=28
```

Generate a strong `JWT_SECRET` with:

```bash
openssl rand -hex 24
```

> Keep `.env` private — it contains your database password and secret key. Don't
> share it. Use one value per line, `KEY=value`, with **no spaces around the `=`**
> and **no comments at the end of a value line** (so the systemd service can read
> it).

---

## 2. PostgreSQL database

The program stores everything in PostgreSQL. Install it, then create one database
and one user.

**Install & start:**

```bash
# Ubuntu / Debian
sudo apt update && sudo apt install -y postgresql
sudo systemctl enable --now postgresql

# Fedora
sudo dnf install -y postgresql-server
sudo postgresql-setup --initdb        # one-time, Fedora only
sudo systemctl enable --now postgresql

# Arch
sudo pacman -S --noconfirm postgresql
sudo -iu postgres initdb -D /var/lib/postgres/data   # one-time, Arch only
sudo systemctl enable --now postgresql
```

**Create the database and user** (pick a strong password):

```bash
sudo -u postgres psql <<'SQL'
CREATE USER pos_user WITH PASSWORD 'CHANGE_THIS_PASSWORD';
CREATE DATABASE pos_db OWNER pos_user;
SQL
```

Your `DATABASE_URL` is then:

```
postgres://pos_user:CHANGE_THIS_PASSWORD@localhost:5432/pos_db?sslmode=disable
```

(`sslmode=disable` is fine when the database is on the **same machine**.)

---

## 3. First run

Put the program somewhere permanent, e.g. `/opt/karots-pos/`:

```bash
sudo mkdir -p /opt/karots-pos
sudo cp karots-pos /opt/karots-pos/
sudo cp .env /opt/karots-pos/
cd /opt/karots-pos
chmod +x karots-pos
```

Run the **one-time setup** (this creates the database tables — nothing else, so you
start with an empty catalog and no staff accounts). Because the program doesn't
auto-read `.env`, load it for this command:

```bash
set -a && . ./.env && set +a && ./karots-pos -init
```

You should see `init complete`. Now start it normally:

```bash
set -a && . ./.env && set +a && ./karots-pos
```

Open **http://localhost:3000** in a browser.

**First login.** A fresh shop has **no staff accounts** — only a hidden system
admin that you (the installer) use to get in and set everything up. Sign in with
that account, then create the shop's real users in **Admin → Users** and hand
those credentials to the staff. By default an admin sets each person's PIN and
they just log in with it; to require staff to choose their own PIN on first login,
turn on **Force PIN change on first login** in **Admin → Settings** (you can also
stop cashiers changing their own PIN there).

> 🛟 **System login (installer only — keep this to yourself).** The hidden system
> admin never shows in the user list and can't be edited or deactivated from the
> app, so a shop can never lock itself out. It logs in like any other user
> (phone `0000000001`).
>
> Its **PIN rotates every hour**, so there's no fixed password to leak. On a
> properly built (bootstrapped) shop binary the code is derived per-install: the
> shop reads its **Install ID** off the login screen and reads it to you, and you
> compute the current PIN on your machine with `make support-pin ID=<install-id>`
> (valid for the current hour ± one). For a shop that wants a **fixed** code
> instead, set `POS_SYSTEM_PIN` (and optionally `POS_SYSTEM_PHONE`) in its `.env` —
> that overrides the rotating one. A plain `go build` (not `make bootstrap`) has no
> per-shop secret and falls back to a shared PIN **`2273`**; the server prints which
> mode it's in on every boot. Full scheme: **[`docs/support-pin-scheme.md`](docs/support-pin-scheme.md)**.

> ℹ️ `-init` gives you a clean, empty shop. If you instead want sample data to try
> the system out, use `-seed` (demo products, suppliers and customers) — but don't
> run it on a real install.

---

## 3a. Going live with a shop that already has stock

A running shop already owns inventory and has hundreds of products to add. Two
features make the switch painless:

**Load the products, then the stock you already have.** Add products in
**Admin → Products** (or import gradually). To record the quantities currently on
the shelf — stock you owned *before* this system existed — use
**Admin → Stock-take** (`/admin/stock/take`): search, type the **counted quantity**
(and optionally the **cost**) for each product, and **Save**. This is *not* booked
as a purchase, so it doesn't inflate expenses or create a supplier debt — it just
sets your opening on-hand. (For a single product, **Stock → Adjust** does the same.)
Set the **cost** so that when this stock later sells, its profit is correct.

**You will miss some products — that's fine.** When a customer brings an item that
isn't in the system yet, the cashier doesn't have to turn them away or stop to do a
full product setup. At the till they hit **+ Quick item** (or it pops up
automatically when a scan finds nothing), type just a **name and price** (barcode
optional — scan it, generate one, or leave it for later), and sell it. The item is
created on the spot and **flagged for review**.

Back in the office, a banner on the **Dashboard** and the **Admin → Items to Review**
list show every quick-added item and **who** rang it up. Click **Finish setup** to
give it a real category, unit and cost; saving also corrects the temporary cost on
the sales it already appeared on, so your **profit figures become accurate**. Then do
a quick physical count and set its stock. After that the item scans like any other —
so the gap closes itself, one item at a time.

> 💡 Until a quick-added item is reviewed its cost is 0, so it shows **100% profit**.
> That's why the dashboard nudges you to finish them — the review list doubles as a
> "numbers not yet trustworthy for these items" worklist.

---

## 4. Run automatically on startup (Linux)

Ubuntu, Fedora, and Arch all use **systemd**, so the same steps work everywhere.
This also solves the settings problem — systemd loads your `.env` for you.

Create `/etc/systemd/system/karots-pos.service`:

```ini
[Unit]
Description=Karots POS
After=network.target postgresql.service
Wants=postgresql.service

[Service]
Type=simple
User=karots
WorkingDirectory=/opt/karots-pos
EnvironmentFile=/opt/karots-pos/.env
ExecStart=/opt/karots-pos/karots-pos
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

> Replace `User=karots` with the Linux user you want it to run as (it must be able
> to read `/opt/karots-pos/` and print). `EnvironmentFile` is what loads your `.env`
> — that's why the `set -a && . ./.env` trick isn't needed here.

Enable and start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now karots-pos
sudo systemctl status karots-pos        # check it's running
journalctl -u karots-pos -f             # watch live logs
```

It will now start on every boot and restart itself if it ever crashes.

---

## 5. Open the till from other devices (optional)

By default the till is reachable at `http://localhost:3000` on the same machine.
To use it from another computer/tablet on the same shop network, find the machine's
IP (`ip addr` / `hostname -I`) and open `http://THAT-IP:3000`. Make sure the port
is allowed through the firewall:

```bash
# Ubuntu (ufw)
sudo ufw allow 3000/tcp
# Fedora / Arch (firewalld)
sudo firewall-cmd --add-port=3000/tcp --permanent && sudo firewall-cmd --reload
```

---

## 6. Printer setup (receipts & labels)

The program prints receipts and barcode labels **directly to the printer** — no
browser print dialog. On Linux this uses **CUPS**, the standard Linux printing
system.

**Install CUPS:**

```bash
sudo apt install -y cups        # Ubuntu
sudo dnf install -y cups        # Fedora
sudo pacman -S --noconfirm cups # Arch
sudo systemctl enable --now cups
```

**Create a *raw* print queue.** This is the one rule that matters: the queue must
be **raw** (no printer "driver"). A raw queue passes the receipt data through
untouched; adding a driver makes it print garbage or eject a metre of paper.

```bash
lpinfo -v                                  # find your printer's address (URI)
sudo lpadmin -p POS80 -E -v usb://Printer/POS-80 -m raw
sudo lpadmin -d POS80                       # optional: make it the default
```

Replace `POS80` with any name you like and the `usb://...` part with the URI from
`lpinfo -v`. For a barcode-label printer, make a second raw queue (e.g. `XP365`).

**Tell the app which queue to use:** open **Admin → Settings → Printers & Labels**.
The fields auto-suggest every printer the system detects — pick your receipt
printer and (if you have one) your label printer, then save. Leave a field blank to
use the system default printer. **No environment variable is needed** — printer
selection lives entirely in Settings.

**Network printers (printer with its own IP / Ethernet / Wi-Fi):** instead of
picking a detected printer, type its address in the field as
`tcp://192.168.1.50:9100` (use your printer's IP; `9100` is the standard port). The
program sends straight to it over the network — no CUPS queue needed for this.

**A printer at each counter (multiple cashiers):** if cashiers work from different
PCs and each counter has its own printer, give each cashier their own login and set
**Admin → Users → edit user → "Receipt printer (this counter)"** to that counter's
**network** printer (`tcp://<counter-ip>:9100`). Each cashier's bills then print at
their counter; anyone left blank uses the shop's default Settings printer. (Paper
size, logo and footer always come from Settings.)

**Skip the print prompt:** by default a finished sale asks *Print / New Sale*. To
print automatically instead, turn off **Admin → Settings → "Ask to print after each
sale"**.

**Cash drawer (optional):** if a cash drawer is wired into the receipt printer's
RJ-11 port, turn on **Admin → Settings → "Open the cash drawer on till cash
events"** and the drawer pops automatically on cash sales, deposits/withdrawals,
credit collected and no-sale — no extra hardware setup. Use the **Test print**
button to confirm it's wired.

> For barcode labels, sticker sizes, the receipt paper width (80mm/58mm), shop
> logo, the cash drawer, and detailed troubleshooting, see **[`PRINTING.md`](PRINTING.md)**.

**Quick troubleshooting:**
- *Empty suggestions in Settings* → CUPS isn't installed, or you haven't added a queue.
- *Garbage characters / huge paper feed* → the queue isn't raw anymore (a driver
  got added). Delete and recreate it with `-m raw`.
- *Nothing prints* → `lpstat -p` (is the queue enabled?), `lpstat -o` (stuck jobs?),
  and check the printer is on and connected.

---

## 7. Backups

### Automatic backups (built in)

When `BACKUP_DIR` is set (see §1), the program automatically saves a full backup
to that folder on startup and then every `BACKUP_INTERVAL` (default every 6 hours),
keeping the most recent `BACKUP_KEEP` files (default 28) and deleting older ones.
These files are **identical** to the ones you get from **Admin → Settings → Backup**.

**To restore:** Admin → Settings → **Restore**, and upload the most recent
`pos-backup-*.json.gz` file from your backup folder.

> ⚠️ Restore **replaces all current data** with the backup's contents. Anything
> that happened after that backup was taken is lost. With the 6-hour default, the
> latest auto-backup could be up to ~6 hours behind — lower `BACKUP_INTERVAL` (e.g.
> `1h`) if you need a tighter safety net, and click the manual Backup button before
> doing anything risky.

### Off-site copies (strongly recommended)

A backup sitting on the **same machine** won't survive a dead disk or a stolen PC.
Copy your backups to another machine automatically. Here's the standard way using
`rsync` over SSH (works the same with `scp`).

**One-time: set up passwordless login to the backup machine.**

```bash
ssh-keygen -t ed25519          # press Enter through the prompts (no passphrase for automation)
ssh-copy-id backupuser@BACKUP-HOST   # enter the remote password once
ssh backupuser@BACKUP-HOST 'echo it works'   # verify
```

**Test the copy by hand:**

```bash
rsync -az /var/lib/karots-pos/backups/ backupuser@BACKUP-HOST:/home/backupuser/pos-backups/
```

**Schedule it with cron** — run `crontab -e` and add (this runs every hour):

```cron
0 * * * * rsync -az /var/lib/karots-pos/backups/ backupuser@BACKUP-HOST:/home/backupuser/pos-backups/ >> /var/log/pos-backup-sync.log 2>&1
```

> `rsync`/`scp`/`cron` are standard Linux tools, not part of the program — this is
> an operating-system task you set up once. `rsync -az` only transfers new/changed
> files and compresses them, so it's cheap to run often.

---

## 8. Windows

> **Windows 10 or 11 only.** Windows 7/8/8.1 are **not** supported — Go 1.21+
> (this build uses 1.26) requires Windows 10 / Server 2016+, and so do current
> PostgreSQL and Chromium. The `.exe` will not start on Windows 7.

> **Automated install (recommended):** put `karots-pos.exe` and **`install.ps1`**
> next to each other and run, from an **elevated** PowerShell:
> ```powershell
> powershell -ExecutionPolicy Bypass -File install.ps1
> ```
> It installs PostgreSQL + Chromium (winget), creates `pos_db`, copies the exe to
> `C:\karots-pos` with a generated `.env` + `backups\`, runs `-init`, registers a
> boot Scheduled Task, and sets up a Chromium kiosk. The manual steps below are the
> equivalent if you'd rather do it by hand.

The program runs on Windows, **including receipt and barcode-label printing** — it
talks to the Windows print spooler directly (RAW mode), so it works the same as on
Linux. Network printers (`tcp://IP:9100`) also work identically.

**Build the Windows program** (we do this for you with `make build-windows`, which
produces `bin/karots-pos.exe`).

**Set the settings.** Windows doesn't use `.env` files for services. The simplest
way is to set the variables system-wide (run PowerShell as Administrator):

```powershell
setx /M DATABASE_URL "postgres://pos_user:PASSWORD@localhost:5432/pos_db?sslmode=disable"
setx /M JWT_SECRET "PASTE_A_LONG_RANDOM_STRING_HERE"
setx /M APP_ENV "production"
```

Install **PostgreSQL for Windows** (from the official installer), then create the
`pos_db` database and `pos_user` user the same way as §2 (using pgAdmin or `psql`).

**First run** (in a regular PowerShell window, after the `setx` step — open a *new*
window so the variables apply):

```powershell
.\karots-pos.exe -init     # one-time setup (creates your first admin)
.\karots-pos.exe           # start it; open http://localhost:3000
```

**Run on startup:** use **NSSM** (the "Non-Sucking Service Manager", free) to run
`karots-pos.exe` as a Windows service, or create a **Task Scheduler** task set to
"run at startup." Both will keep it running in the background and restart it on boot.

**Printing on Windows.** Install your thermal/label printer in Windows normally
(Settings → Bluetooth & devices → Printers). If the vendor has a driver, use it; if
in doubt, the **"Generic / Text Only"** driver is the most reliable for raw thermal
printing. Then open Admin → Settings → Printers & Labels and pick the printer (the
field auto-suggests installed printers, same as Linux), or type a network printer as
`tcp://192.168.1.50:9100`. If receipts print garbled, switch that printer to the
"Generic / Text Only" driver and try again.

---

## 9. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `JWT_SECRET must be at least 32 characters` | Make your secret longer (`openssl rand -hex 24`). |
| `required env var DATABASE_URL is not set` | Settings weren't loaded. Use the systemd service, or `set -a && . ./.env && set +a` before running. |
| Can't connect to the database | Postgres not running (`systemctl status postgresql`), or wrong password/host in `DATABASE_URL`. |
| `address already in use` | Port 3000 is taken — change `SERVER_PORT`, or stop whatever is using it. |
| Printer dropdown is empty | CUPS not installed, or no print queue added (see §6). |
| Receipts print garbage / huge feed | The print queue isn't *raw* — recreate it with `-m raw` (see §6 / `PRINTING.md`). |
| Forgot a login | Use the Admin account to reset PINs in Admin → Users. If the Admin is locked out, contact us. |

Still stuck? Save the program's logs (`journalctl -u karots-pos` on Linux) and
contact us.
