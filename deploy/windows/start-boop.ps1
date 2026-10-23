# Starts the Boop hub in the background if it is not already running.
# Run by start-boop-hidden.vbs (no window) from the "Boop alert hub" scheduled
# task at sign-in; safe to run again at any time. Works in Windows PowerShell
# 5.1 and PowerShell 7.
#
# Settings come from boop.env next to this script (KEY=VALUE lines).
$ErrorActionPreference = 'Stop'
$dir = $PSScriptRoot
$envFile = Join-Path $dir 'boop.env'
if (-not (Test-Path $envFile)) { throw "Missing $envFile (run install.ps1 first)" }

foreach ($line in Get-Content $envFile) {
  $t = $line.Trim()
  if ($t -eq '' -or $t.StartsWith('#')) { continue }
  $i = $t.IndexOf('=')
  if ($i -lt 1) { continue }
  [Environment]::SetEnvironmentVariable($t.Substring(0, $i).Trim(), $t.Substring($i + 1).Trim(), 'Process')
}

$port = [int]$env:BOOP_PORT
if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) { exit 0 }

$logs = Join-Path $dir 'logs'
New-Item -ItemType Directory -Force $logs | Out-Null
foreach ($name in 'boop.log', 'boop.err.log') {
  $f = Join-Path $logs $name
  if (Test-Path $f) { Move-Item -Force $f "$f.1" }
}

Start-Process -FilePath (Join-Path $dir 'bin\boop.exe') -WorkingDirectory $dir -WindowStyle Hidden `
  -RedirectStandardOutput (Join-Path $logs 'boop.log') -RedirectStandardError (Join-Path $logs 'boop.err.log')

# Keep the tailnet HTTPS address pointing at the hub (Tailscale keeps this
# across reboots; setting it again is harmless and repairs it if cleared).
$ts = 'C:\Program Files\Tailscale\tailscale.exe'
if ($env:BOOP_TAILSCALE_PORT -and (Test-Path $ts)) {
  & $ts serve --bg "--https=$($env:BOOP_TAILSCALE_PORT)" "http://127.0.0.1:$port" | Out-Null
}
