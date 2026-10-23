<#
.SYNOPSIS
  Installs (or updates) the Boop hub on this Windows PC as a background app
  that starts at sign-in with no window and restarts itself if it stops.

.DESCRIPTION
  - Copies boop.exe and the start scripts into -Dir (default %LOCALAPPDATA%\boop).
  - Creates boop.env on first install: port, database path, a login with a
    random password, and (with -TailscalePort) the tailnet HTTPS address.
    An existing boop.env is never overwritten.
  - Registers the "Boop alert hub" scheduled task: at sign-in, and every 5
    minutes as a watchdog (it does nothing while the hub is running).
  - With -TailscalePort, serves the hub on that tailnet-only HTTPS port.
  - Starts the hub and waits until /health answers.
  Re-running it updates the binary and scripts and restarts the hub.
  Works in Windows PowerShell 5.1 and PowerShell 7. No administrator needed.

.EXAMPLE
  .\install.ps1 -Binary ..\..\bin\boop.exe -TailscalePort 10002
#>
param(
  [string]$Binary = '',
  [int]$Port = 4795,
  [string]$Dir = (Join-Path $env:LOCALAPPDATA 'boop'),
  [int]$TailscalePort = 0,
  [string]$AdminUser = $env:USERNAME.ToLower(),
  [string]$TaskName = 'Boop alert hub'
)
$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$repo = Resolve-Path (Join-Path $here '..\..')

foreach ($d in $Dir, "$Dir\bin", "$Dir\data", "$Dir\logs") { New-Item -ItemType Directory -Force $d | Out-Null }

# 1. The binary: given, or built from this repository (needs Go; the web UI
#    must already be built into server/internal/web/dist).
if (-not $Binary) {
  $go = Get-Command go -ErrorAction SilentlyContinue
  if (-not $go) { throw 'Pass -Binary <path to boop.exe>, or install Go so this script can build it.' }
  $Binary = Join-Path $env:TEMP 'boop-build.exe'
  Push-Location (Join-Path $repo 'server')
  $env:CGO_ENABLED = '0'
  & go build -trimpath -ldflags '-s -w' -o $Binary ./cmd/boop
  Pop-Location
  if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
}
if (-not (Test-Path $Binary)) { throw "Binary not found: $Binary" }

# 2. Stop a running hub from this folder so its binary can be replaced.
$exe = Join-Path $Dir 'bin\boop.exe'
$running = Get-Process boop -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe }
if ($running) {
  $running | Stop-Process -Force
  Start-Sleep -Seconds 1
}
Copy-Item -Force $Binary $exe
Copy-Item -Force (Join-Path $here 'start-boop.ps1') $Dir
Copy-Item -Force (Join-Path $here 'start-boop-hidden.vbs') $Dir

# 3. Settings, written once.
$envFile = Join-Path $Dir 'boop.env'
if (-not (Test-Path $envFile)) {
  $bytes = New-Object byte[] 18
  [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
  $password = [Convert]::ToBase64String($bytes).Replace('+', 'x').Replace('/', 'y')
  $lines = @(
    '# Boop hub settings. After changing them, run install.ps1 again to restart the hub.',
    "BOOP_PORT=$Port",
    "BOOP_DATABASE_PATH=$Dir\data\boop.db",
    'BOOP_LOG_LEVEL=info',
    '# Login for the web UI and the phone app.',
    "BOOP_ADMIN_USER=$AdminUser",
    "BOOP_ADMIN_PASSWORD=$password"
  )
  if ($TailscalePort -gt 0) {
    $dns = ''
    try { $dns = ((& 'C:\Program Files\Tailscale\tailscale.exe' status --json | ConvertFrom-Json).Self.DNSName).TrimEnd('.') } catch { }
    if (-not $dns) { throw 'Tailscale is not running or not signed in; start it or install without -TailscalePort.' }
    $lines += "BOOP_BASE_URL=https://${dns}:$TailscalePort"
    $lines += "BOOP_TAILSCALE_PORT=$TailscalePort"
  }
  Set-Content -Path $envFile -Value $lines -Encoding ASCII
  Write-Host "Created $envFile (the login password is in it)."
}

# 4. Start at sign-in, plus a 5-minute watchdog that restarts it if it stops.
$action = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument "`"$Dir\start-boop-hidden.vbs`""
$atLogon = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
$watchdog = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 5)
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Minutes 2)
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger @($atLogon, $watchdog) -Settings $settings `
  -Description 'Starts the Boop alert hub with no window (and restarts it if it stopped).' -Force | Out-Null

# 5. Start it now and wait for it to answer.
Start-ScheduledTask -TaskName $TaskName
$port = ((Get-Content $envFile | Where-Object { $_ -match '^BOOP_PORT=' }) -replace '^BOOP_PORT=', '').Trim()
$ok = $false
for ($i = 0; $i -lt 30 -and -not $ok; $i++) {
  Start-Sleep -Seconds 1
  try { $ok = (Invoke-RestMethod "http://127.0.0.1:$port/health" -TimeoutSec 2).status -eq 'ok' } catch { }
}
if (-not $ok) { throw "The hub did not answer on port $port; see $Dir\logs\boop.err.log" }
$base = ((Get-Content $envFile | Where-Object { $_ -match '^BOOP_BASE_URL=' }) -replace '^BOOP_BASE_URL=', '').Trim()
Write-Host "Boop is running: http://127.0.0.1:$port"
if ($base) { Write-Host "From your phone (tailnet): $base" }
Write-Host "Settings and login: $envFile"
