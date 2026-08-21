<#
.SYNOPSIS
  Bring up an AO-owned Paseo daemon on THIS Windows machine.

.DESCRIPTION
  The Windows counterpart of paseo-host-setup.sh: same posture, same guards, same
  printed `ao remote register` line. See docs/paseo-integration/SECURITY.md §3.

  A host needs Paseo (a Node CLI) and the agent CLIs it will run. It does NOT need
  Go, this repository, or the `ao` binary — those belong to the AO machine. The
  daemon started here is headless by design: no web UI, no relay, no MCP.

  Windows works because node-pty ships a win32-x64 conpty prebuild, so nothing is
  compiled at install time and Visual Studio Build Tools are not needed.

.PARAMETER Command
  start (default) | status | stop | scheduled-task

.PARAMETER Listen
  Listen address, host:port. Default 127.0.0.1:6780 (loopback only).
  For a remote AO machine, bind the Tailscale address: -Listen 100.x.y.z:6780

.PARAMETER HostHome
  PASEO_HOME for this daemon. Default $env:USERPROFILE\.paseo-ao
  Never ~/.paseo — that is the desktop app's home and AO refuses desktop-managed
  daemons.

.PARAMETER HostId
  Host id used in the printed register line. Default: lowercased hostname.

.EXAMPLE
  .\scripts\paseo-host-setup.ps1
.EXAMPLE
  .\scripts\paseo-host-setup.ps1 -Listen 100.64.0.5:6780 -HostId office-win
.EXAMPLE
  .\scripts\paseo-host-setup.ps1 status
#>
[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [ValidateSet('start', 'status', 'stop', 'scheduled-task')]
  [string]$Command = 'start',

  [string]$Listen   = '127.0.0.1:6780',
  [string]$HostHome = (Join-Path $env:USERPROFILE '.paseo-ao'),
  [string]$HostId   = $env:COMPUTERNAME.ToLower()
)

$ErrorActionPreference = 'Stop'

# AO pins the CLI contract to one version: adapters/execution/paseo/version.go
# compares `paseo --version` for EQUALITY and refuses anything else, because the
# JSON shapes it parses are fixture-verified against this build alone.
$PinnedPaseoVersion = '0.2.5'

$PwFile = Join-Path $HostHome 'daemon-password'

function Write-Note { param($m) Write-Host "-- $m" -ForegroundColor Cyan }
function Write-Ok   { param($m) Write-Host "ok $m"  -ForegroundColor Green }
function Write-Warn { param($m) Write-Host "warning $m" -ForegroundColor Yellow }
function Die        { param($m) Write-Host "error $m" -ForegroundColor Red; exit 1 }

# The default home belongs to the Paseo desktop app, whose daemon reports
# desktopManaged=true. AO refuses to drive that daemon outright
# (adapters/execution/paseo/backend.go), so pointing this script at ~/.paseo
# would produce a host that registers and then refuses every dispatch.
if ($HostHome.TrimEnd('\') -eq (Join-Path $env:USERPROFILE '.paseo').TrimEnd('\')) {
  Die "refusing to use ~\.paseo: that is the desktop app's home, and AO refuses desktop-managed daemons. Pick another -HostHome."
}

function Require-Paseo {
  $cmd = Get-Command paseo -ErrorAction SilentlyContinue
  if (-not $cmd) {
    Die "paseo not on PATH. Install it with: npm install -g @getpaseo/cli@$PinnedPaseoVersion`n         If npm itself is missing, see the Windows host section of README.md."
  }
  $found = (& paseo --version 2>&1 | Out-String).Trim()
  if ($found -ne $PinnedPaseoVersion) {
    Die "paseo $found is installed but AO pins $PinnedPaseoVersion exactly (it will report the host unsupported). Fix with: npm install -g @getpaseo/cli@$PinnedPaseoVersion"
  }
  Write-Ok "paseo $found"
}

# Every AO invocation of the CLI scrubs Paseo's ambient variables (DECISIONS D23):
# a leaked PASEO_AGENT_ID silently reparents every new agent into the caller's
# workspace, where `ls` cannot see it. Do the same when starting the daemon.
function Clear-PaseoAmbient {
  foreach ($v in 'PASEO_AGENT_ID', 'PASEO_WORKSPACE_ID', 'PASEO_HOST', 'PASEO_SERVER_ID', 'PASEO_LISTEN') {
    Remove-Item "env:$v" -ErrorAction SilentlyContinue
  }
}

# Windows has no chmod; drop inheritance and grant the current user only.
function Protect-File {
  param([string]$Path)
  & icacls $Path /inheritance:r /grant:r "$($env:USERNAME):(R,W)" | Out-Null
}

function Write-HostConfig {
  # Belt and braces for the flags below. These are PERSISTED defaults, and the
  # stock ones fail open: relay.enabled defaults true and dials out at boot, and
  # cors.allowedOrigins defaults to ["https://app.paseo.sh"] — any JS on that
  # origin gets a scopes:["*"] session on this daemon with no password. A future
  # `paseo daemon start` without the flags must not silently restore either one.
  #
  # features.dictation / features.voiceMode are off because they default ON with
  # provider "local": a stock daemon downloads ~983 MB of speech models
  # (parakeet-tdt-0.6b-v2-int8 + kokoro-en-v0_19) into $PASEO_HOME\models on its
  # first boot. A headless worker never uses them.
  $cfg = @"
{
  "version": 1,
  "daemon": {
    "listen": "$Listen",
    "cors": { "allowedOrigins": [] },
    "relay": { "enabled": false },
    "mcp": { "enabled": false, "injectIntoAgents": false },
    "browserTools": { "enabled": false }
  },
  "features": {
    "dictation": { "enabled": false },
    "voiceMode": { "enabled": false }
  }
}
"@
  $path = Join-Path $HostHome 'config.json'
  [System.IO.File]::WriteAllText($path, $cfg, (New-Object System.Text.UTF8Encoding($false)))
  Protect-File $path
}

function Get-ProbeHost {
  if ($Listen -match '^(0\.0\.0\.0|\[::\]|::):(\d+)$') { return "127.0.0.1:$($Matches[2])" }
  return $Listen
}

function Get-Password {
  if (-not (Test-Path $PwFile)) { Die "no daemon password at $PwFile; run 'start' first" }
  # Read exact bytes: no trailing newline, no BOM. The AO machine's secret-ref
  # file must match this byte for byte or the probe 401s.
  return [System.IO.File]::ReadAllText($PwFile)
}

function Invoke-Start {
  Require-Paseo
  if ($Listen -notmatch ':') {
    Die "-Listen must be host:port. Paseo resolves a colonless host to the LOCAL daemon, which would run remote work on the wrong machine."
  }

  New-Item -ItemType Directory -Force $HostHome | Out-Null

  if (-not (Test-Path $PwFile)) {
    $bytes = New-Object byte[] 24
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    $pw = [BitConverter]::ToString($bytes).Replace('-', '').ToLower()   # hex, like openssl rand -hex 24
    [System.IO.File]::WriteAllText($PwFile, $pw, (New-Object System.Text.UTF8Encoding($false)))
    Write-Note "generated a new daemon password at $PwFile"
  }
  Protect-File $PwFile
  $pw = Get-Password

  Write-HostConfig

  switch -Regex ($Listen) {
    '^(127\.0\.0\.1|localhost|\[::1\]):' { Write-Ok 'listening on loopback only' }
    '^(0\.0\.0\.0|\[::\]|::):' {
      Write-Warn 'listening on ALL interfaces. SECURITY.md §3 says never a LAN interface: the daemon is plaintext HTTP with no TLS, and the password buys terminal write access. Bind one address (a Tailscale IP) instead.'
    }
    default {
      Write-Warn "listening on $Listen - reachable off-box. Only do this over Tailscale or an equally private network; there is no TLS."
    }
  }

  # PASEO_PASSWORD lands in the daemon's environment and stock Paseo 0.2.5 strips
  # only five runtime-control keys before spawning an agent, so every agent on
  # this host can read it with `printenv` - and thus so can that agent's model
  # vendor (SECURITY.md §6). We do NOT patch the installed Paseo (AGPL §13).
  # Treat this password as compromised-by-design: scope it to this daemon, never
  # reuse it, rotate by deleting the file above.
  Clear-PaseoAmbient
  $env:PASEO_HOME     = $HostHome
  $env:PASEO_PASSWORD = $pw

  & paseo daemon start --home $HostHome --listen $Listen --no-relay --no-mcp --no-inject-mcp --no-web-ui | Out-Null
  if ($LASTEXITCODE -ne 0) { Die "paseo daemon start failed ($LASTEXITCODE). Logs: $HostHome\daemon.log" }

  $probe = Get-ProbeHost
  # /api/health is the password-exempt readiness route; /api/status needs the bearer.
  for ($i = 0; $i -lt 45; $i++) {
    try { Invoke-RestMethod "http://$probe/api/health" -TimeoutSec 2 | Out-Null; break } catch { Start-Sleep -Milliseconds 1000 }
  }
  $status = $null
  try { $status = Invoke-RestMethod "http://$probe/api/status" -Headers @{ Authorization = "Bearer $pw" } -TimeoutSec 5 } catch {}
  if (-not $status) { Die "daemon did not answer /api/status. Logs: $HostHome\daemon.log" }
  Write-Ok "daemon up: serverId=$($status.serverId) version=$($status.version) listen=$($status.listen)"

  # An unauthenticated /api/status must fail. If it does not, PASEO_PASSWORD never
  # reached the daemon and this host is wide open to anyone who can route to it.
  try {
    Invoke-RestMethod "http://$probe/api/status" -TimeoutSec 5 | Out-Null
    Write-Warn 'UNAUTHENTICATED /api/status succeeded - the daemon password is NOT being enforced. Do not expose this address.'
  } catch { Write-Ok 'auth enforced: unauthenticated /api/status rejected' }

  Write-Host "`nOn the AO machine, store the password as a secret ref and register this computer:`n" -ForegroundColor White
  @"
  mkdir -p ~/.ao/secrets && chmod 700 ~/.ao/secrets
  printf '%s' '<the contents of $PwFile on this host>' > ~/.ao/secrets/$HostId-pw
  chmod 600 ~/.ao/secrets/$HostId-pw

  ao remote register $HostId --name "$HostId" \
    --transport tailscale --endpoint $Listen \
    --secret-ref $HostId-pw --trust-zone hobby --max-sessions 3

  ao remote bind <project-id> $HostId \
    --host-path <absolute path of that repo ON THIS HOST> --base-branch main
"@
  Write-Host "`nCopy the password over a private channel; never paste it into --endpoint or a command AO records."
  Write-Host "Windows host paths use backslashes and a drive letter (C:\Users\you\code\repo) - not a \\wsl.localhost path, which git worktrees cannot use reliably."
}

function Invoke-Status {
  $pw = Get-Password
  $probe = Get-ProbeHost
  try {
    Invoke-RestMethod "http://$probe/api/status" -Headers @{ Authorization = "Bearer $pw" } -TimeoutSec 5 | ConvertTo-Json -Compress
  } catch { Die "no answer from http://$probe/api/status" }
}

function Invoke-Stop {
  Require-Paseo
  Clear-PaseoAmbient
  $env:PASEO_HOME = $HostHome
  # Addressed by --home, which cannot reach the desktop app's daemon.
  & paseo daemon stop --home $HostHome
}

function Show-ScheduledTask {
  # The Windows equivalent of the systemd user unit. A scheduled task does not
  # read your shell profile, so PATH must be explicit or an npm-global paseo and
  # the agent CLIs are invisible to it.
  $paseoCmd = (Get-Command paseo -ErrorAction SilentlyContinue).Source
  if (-not $paseoCmd) { $paseoCmd = 'paseo' }
  $paseoDir = Split-Path $paseoCmd -Parent
  @"
# Run the host daemon at logon. Wrapper first, so PATH and the password are set:
#   $HostHome\start-host.ps1
#
# ---8<--- $HostHome\start-host.ps1
`$env:Path = '$paseoDir;' + `$env:Path
`$env:PASEO_HOME = '$HostHome'
`$env:PASEO_PASSWORD = [System.IO.File]::ReadAllText('$PwFile')
foreach (`$v in 'PASEO_AGENT_ID','PASEO_WORKSPACE_ID','PASEO_HOST','PASEO_SERVER_ID') { Remove-Item "env:`$v" -ErrorAction SilentlyContinue }
& paseo daemon start --home '$HostHome' --listen '$Listen' --no-relay --no-mcp --no-inject-mcp --no-web-ui
# --->8---
#
# Then register it (no admin needed for a logon task in your own account):
schtasks /Create /TN "PaseoAOHost" /SC ONLOGON /RL LIMITED ``
  /TR "powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$HostHome\start-host.ps1`"" ``
  /F

# Verify:   schtasks /Query /TN "PaseoAOHost"
# Remove:   schtasks /Delete /TN "PaseoAOHost" /F
#
# Note: a logon task starts the daemon only after you sign in. The agent CLIs
# need a logged-in user session anyway (claude stores credentials per user), so a
# SYSTEM-level service would not be able to run work.
"@
}

switch ($Command) {
  'start'          { Invoke-Start }
  'status'         { Invoke-Status }
  'stop'           { Invoke-Stop }
  'scheduled-task' { Show-ScheduledTask }
}
