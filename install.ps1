<#
  Karots POS - one-shot installer for a fresh Windows 10/11 PC.

  The Windows counterpart of install.sh. Drop this script and karots-pos.exe next
  to each other and run it from an ELEVATED PowerShell:

      powershell -ExecutionPolicy Bypass -File install.ps1

  What it does (idempotent - safe to re-run):
    1. Installs PostgreSQL and Chromium via winget.
    2. Creates the pos_db database + pos_user role (generates a strong password).
    3. Copies the binary to C:\karots-pos, writes a production .env next to it
       (the exe auto-loads .env from beside itself) with a generated JWT secret
       and a backups\ folder, and runs the one-time -init.
    4. Registers a Scheduled Task so the till starts on boot (auto-restarts).
    5. Sets up a Chromium --kiosk that opens the till full-screen at login.

  Printing works out of the box (Windows print spooler, RAW). No Go/Node needed.

  IMPORTANT: Windows 7/8/8.1 are NOT supported - Go 1.21+ (this build uses 1.26)
  requires Windows 10 / Server 2016 or newer, and so do current PostgreSQL and
  Chromium. Use Windows 10 or 11.
#>
#Requires -RunAsAdministrator
[CmdletBinding()]
param(
  [string]$Binary     = "",
  [string]$InstallDir = "C:\karots-pos",
  [string]$DbName     = "pos_db",
  [string]$DbUser     = "pos_user",
  [int]   $Port       = 3000,
  [string]$BackupDir  = "",
  [switch]$NoKiosk
)

$ErrorActionPreference = "Stop"
if (-not $BackupDir) { $BackupDir = Join-Path $InstallDir "backups" }
$BinName = "karots-pos.exe"

function Say  ($m) { Write-Host "==> $m" -ForegroundColor Cyan }
function Warn ($m) { Write-Host "!   $m" -ForegroundColor Yellow }
function Die  ($m) { Write-Host "ERROR: $m" -ForegroundColor Red; exit 1 }

function New-Secret([int]$bytes) {
  $b = New-Object byte[] $bytes
  [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
  ($b | ForEach-Object { $_.ToString("x2") }) -join ""
}

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
$os = [System.Environment]::OSVersion.Version
if ($os.Major -lt 10) {
  Die "Windows 10 or newer is required (this is $($os.Major).$($os.Minor)). Go 1.21+ dropped Windows 7/8, so karots-pos.exe will not run there. Please use Windows 10/11."
}
if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
  Die "winget not found. Install 'App Installer' from the Microsoft Store (Windows 10 1809+/11), then re-run."
}

# Locate the binary: -Binary arg -> beside this script -> Downloads.
if (-not $Binary) {
  $candidates = @(
    (Join-Path $PSScriptRoot $BinName),
    (Join-Path ([Environment]::GetFolderPath("UserProfile")) "Downloads\$BinName"),
    (Join-Path (Get-Location) $BinName)
  )
  $Binary = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
}
if (-not $Binary -or -not (Test-Path $Binary)) {
  Die "binary not found. Put $BinName next to install.ps1 (or in Downloads), or pass -Binary <path>."
}
Say "Using binary:  $Binary"
Say "Install dir:   $InstallDir"

# ---------------------------------------------------------------------------
# 1. Packages (winget)
# ---------------------------------------------------------------------------
function Winget-Install($id, $custom) {
  Say "Installing $id ..."
  $wa = @("install","--id",$id,"-e","--silent","--accept-package-agreements","--accept-source-agreements")
  if ($custom) { $wa += @("--custom",$custom) }
  & winget @wa 2>&1 | Out-Null
  # winget returns non-zero when the package is already installed - not an error.
}

$PgSuper = New-Secret 16   # superuser password used only if winget installs PG now
Winget-Install "PostgreSQL.PostgreSQL" "--superpassword $PgSuper --serverport 5432"

# Chromium (fall back to Google Chrome, which is the most reliable on winget).
$installedChromium = $true
try { Winget-Install "Hibbiki.Chromium" $null } catch { $installedChromium = $false }
# Resolve a usable browser; if none, try Chrome.
function Resolve-Browser {
  @(
    "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
    "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe",
    "$env:LOCALAPPDATA\Chromium\Application\chrome.exe",
    "$env:ProgramFiles\Chromium\Application\chrome.exe",
    "${env:ProgramFiles(x86)}\Chromium\Application\chrome.exe"
  ) | Where-Object { Test-Path $_ } | Select-Object -First 1
}
$Browser = Resolve-Browser
if (-not $Browser) { Winget-Install "Google.Chrome" $null; $Browser = Resolve-Browser }
if (-not $Browser) { Warn "No Chromium/Chrome found - the kiosk step will be skipped." }

# Locate psql.exe from the freshly installed PostgreSQL.
$psql = Get-ChildItem "C:\Program Files\PostgreSQL\*\bin\psql.exe" -ErrorAction SilentlyContinue |
        Sort-Object FullName -Descending | Select-Object -First 1
if (-not $psql) { Die "psql.exe not found under C:\Program Files\PostgreSQL - PostgreSQL install may have failed." }
$psql = $psql.FullName

# ---------------------------------------------------------------------------
# 2. Database (reuse existing creds on re-run)
# ---------------------------------------------------------------------------
$envFile = Join-Path $InstallDir ".env"
$DbPass = ""; $JwtSecret = ""
if (Test-Path $envFile) {
  Say "Existing .env found - reusing its database password and JWT secret."
  $txt = Get-Content $envFile -Raw
  if ($txt -match 'DATABASE_URL=.*://[^:]+:([^@]+)@') { $DbPass = $Matches[1] }
  if ($txt -match '(?m)^JWT_SECRET=(.+)$')            { $JwtSecret = $Matches[1].Trim() }
}
if (-not $DbPass)    { $DbPass = New-Secret 16 }
if (-not $JwtSecret) { $JwtSecret = New-Secret 24 }   # 48 hex chars >= 32 required

function Test-PgLogin($user, $pass, $db) {
  $env:PGPASSWORD = $pass
  $out = & $psql -U $user -h localhost -p 5432 -d $db -tAc "SELECT 1" 2>$null
  Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
  return ($LASTEXITCODE -eq 0 -and ($out -join "").Trim() -eq "1")
}

if (Test-PgLogin $DbUser $DbPass $DbName) {
  Say "Database '$DbName' and role '$DbUser' already usable - skipping DB setup."
} else {
  # Need the superuser. Use the password we just set; if PG pre-existed, ask.
  $super = $PgSuper
  if (-not (Test-PgLogin "postgres" $super "postgres")) {
    Warn "Could not log in as 'postgres' with the generated password (PostgreSQL was probably already installed)."
    $sec = Read-Host "Enter the existing 'postgres' superuser password" -AsSecureString
    $super = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
               [Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
    if (-not (Test-PgLogin "postgres" $super "postgres")) { Die "postgres password rejected." }
  }
  Say "Creating role '$DbUser' and database '$DbName' ..."
  $env:PGPASSWORD = $super
  $roleSql = "DO `$`$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='$DbUser') THEN CREATE ROLE $DbUser LOGIN PASSWORD '$DbPass'; ELSE ALTER ROLE $DbUser WITH LOGIN PASSWORD '$DbPass'; END IF; END `$`$;"
  & $psql -U postgres -h localhost -p 5432 -d postgres -c $roleSql | Out-Null
  $dbExists = & $psql -U postgres -h localhost -p 5432 -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='$DbName'"
  if (($dbExists -join "").Trim() -ne "1") {
    & $psql -U postgres -h localhost -p 5432 -d postgres -c "CREATE DATABASE $DbName OWNER $DbUser" | Out-Null
  }
  Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
}
$DatabaseUrl = "postgres://${DbUser}:${DbPass}@localhost:5432/${DbName}?sslmode=disable"

# ---------------------------------------------------------------------------
# 3. Files: binary, .env, backups
# ---------------------------------------------------------------------------
Say "Installing binary and settings into $InstallDir ..."
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $BackupDir  | Out-Null
Copy-Item -Path $Binary -Destination (Join-Path $InstallDir $BinName) -Force

# .env - the exe auto-loads it from beside itself. KEY=value, no inline comments.
$envContent = @"
APP_ENV=production
DATABASE_URL=$DatabaseUrl
SERVER_PORT=$Port
JWT_SECRET=$JwtSecret
JWT_EXPIRES_IN=12h
JWT_REFRESH_EXPIRES_IN=168h
CORS_ORIGINS=http://localhost:$Port
COOKIE_SECURE=auto
BACKUP_DIR=$BackupDir
BACKUP_INTERVAL=6h
BACKUP_KEEP=28
"@
Set-Content -Path $envFile -Value $envContent -Encoding ASCII

# One-time setup: schema + hidden admin (migrations also run on every boot).
Say "Running one-time initialisation (-init) ..."
Push-Location $InstallDir
& (Join-Path $InstallDir $BinName) -init
if ($LASTEXITCODE -ne 0) { Warn "-init returned $LASTEXITCODE (often fine if already initialised). Continuing." }
Pop-Location

# ---------------------------------------------------------------------------
# 4. Auto-start on boot (Scheduled Task; a Go exe isn't a native service)
# ---------------------------------------------------------------------------
Say "Registering the boot Scheduled Task 'KarotsPOS' ..."
$logDir = Join-Path $InstallDir "logs"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$exe    = Join-Path $InstallDir $BinName
$logCmd = "/c `"`"$exe`" >> `"$logDir\pos.log`" 2>&1`""
# Run at logon as the interactive user (not SYSTEM): a till is always logged in
# for the kiosk, and running in the user's session means the app sees the printers
# installed for that user (a SYSTEM service often can't see per-user USB printers).
$taskUser  = "$env:USERDOMAIN\$env:USERNAME"
$action    = New-ScheduledTaskAction -Execute "cmd.exe" -Argument $logCmd -WorkingDirectory $InstallDir
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User $taskUser
$principal = New-ScheduledTaskPrincipal -UserId $taskUser -LogonType Interactive -RunLevel Highest
$settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
              -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
              -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName "KarotsPOS" -Action $action -Trigger $trigger `
  -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName "KarotsPOS"

# Firewall: allow other tills on the LAN to reach this server.
if (-not (Get-NetFirewallRule -DisplayName "Karots POS" -ErrorAction SilentlyContinue)) {
  New-NetFirewallRule -DisplayName "Karots POS" -Direction Inbound -Protocol TCP `
    -LocalPort $Port -Action Allow | Out-Null
}

# Wait for the server to answer.
Say "Waiting for the till to come up on http://localhost:$Port ..."
$up = $false
foreach ($i in 1..30) {
  try { Invoke-WebRequest "http://localhost:$Port/health" -UseBasicParsing -TimeoutSec 2 | Out-Null; $up = $true; break }
  catch { Start-Sleep -Seconds 1 }
}
if ($up) { Say "Till is up." } else { Warn "Till did not answer yet - check $logDir\pos.log" }

# ---------------------------------------------------------------------------
# 5. Chromium kiosk at login
# ---------------------------------------------------------------------------
if (-not $NoKiosk -and $Browser) {
  Say "Setting up the Chromium kiosk ..."
  $kioskCmd = Join-Path $InstallDir "kiosk.cmd"
  @"
@echo off
:wait
curl -s -o NUL http://localhost:$Port/health && goto up
timeout /t 2 >NUL
goto wait
:up
start "" "$Browser" --kiosk --app=http://localhost:$Port --incognito --noerrdialogs --disable-infobars --disable-session-crashed-bubble --disable-features=TranslateUI --check-for-update-interval=31536000
"@ | Set-Content -Path $kioskCmd -Encoding ASCII

  # A shortcut in the all-users Startup folder launches it (minimised) at login.
  $startup = [Environment]::GetFolderPath("CommonStartup")
  $lnk = Join-Path $startup "Karots POS Kiosk.lnk"
  $sc  = (New-Object -ComObject WScript.Shell).CreateShortcut($lnk)
  $sc.TargetPath  = $kioskCmd
  $sc.WorkingDirectory = $InstallDir
  $sc.WindowStyle = 7          # minimised (the wait-loop console stays hidden)
  $sc.Save()
  Say "Kiosk installed - opens automatically at the next login. Test now: $kioskCmd"
} else {
  Say "Kiosk skipped."
}

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "OK  Karots POS is installed." -ForegroundColor Green
Write-Host @"

  Till URL      http://localhost:$Port   (and http://<this-pc-ip>:$Port on the LAN)
  Files         $InstallDir   (karots-pos.exe, .env, backups\, logs\)
  Service       Scheduled Task 'KarotsPOS' (starts at login, auto-restarts) - logs in $logDir\pos.log
  First login   hidden system admin - phone 0000000001 (see SETUP.md for the PIN),
                then create real staff in Admin -> Users.
  Printing      install your printer in Windows (for a thermal printer the
                'Generic / Text Only' driver is most reliable), then pick it in
                Admin -> Settings -> Printers. Server-side spooler RAW - no browser,
                no extra install. Network printers: tcp://IP:9100. See PRINTING.md.

  Keep $envFile private - it holds the DB password and JWT secret.
"@
