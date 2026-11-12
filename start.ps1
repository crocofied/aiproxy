<#
.SYNOPSIS
    Starts the lanproxy main node with Docker Desktop (Windows).
.EXAMPLE
    .\start.ps1            build and start, then open the web interface
    .\start.ps1 stop       stop
    .\start.ps1 logs       follow the logs
    .\start.ps1 update     git pull, rebuild, restart
#>
param(
    [ValidateSet('start', 'stop', 'logs', 'update')]
    [string]$Action = 'start'
)

# 'Continue': Docker writes progress to stderr, which must not abort the
# script. Failures are detected through $LASTEXITCODE instead.
$ErrorActionPreference = 'Continue'
Set-Location -Path $PSScriptRoot

function Fail([string]$Message) {
    Write-Host $Message -ForegroundColor Red
    exit 1
}

# --- Docker available and running? ---
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Fail 'Docker was not found. Install Docker Desktop: https://docs.docker.com/desktop/setup/install/windows-install/'
}
docker info *> $null
if ($LASTEXITCODE -ne 0) {
    Fail 'Docker is installed but not running. Start Docker Desktop and try again.'
}
docker compose version *> $null
if ($LASTEXITCODE -ne 0) {
    Fail 'The Docker Compose plugin is missing. Update Docker Desktop.'
}

# --- optional settings, see .env.example ---
if (-not (Test-Path '.env')) {
    Copy-Item '.env.example' '.env'
}
$port = '8080'
$line = Get-Content '.env' | Where-Object { $_ -match '^LANPROXY_UI_PORT=(.+)$' } | Select-Object -Last 1
if ($line) {
    $port = ($line -replace '^LANPROXY_UI_PORT=', '').Trim()
}

switch ($Action) {
    'stop' {
        docker compose down
        exit $LASTEXITCODE
    }
    'logs' {
        docker compose logs -f
        exit $LASTEXITCODE
    }
    'update' {
        git pull --ff-only
        if ($LASTEXITCODE -ne 0) { Fail 'git pull failed.' }
    }
}

# --- ports 80 and 443 must be free (IIS, Skype, other proxies ...) ---
$running = docker ps --filter 'name=^lanproxy$' --format '{{.Names}}'
if (-not $running) {
    foreach ($p in 80, 443) {
        $busy = Get-NetTCPConnection -LocalPort $p -State Listen -ErrorAction SilentlyContinue
        if ($busy) {
            $owner = (Get-Process -Id $busy[0].OwningProcess -ErrorAction SilentlyContinue).ProcessName
            Fail "Port $p is already in use by '$owner'. Stop that program first; lanproxy needs ports 80 and 443."
        }
    }
}

docker compose up -d --build
if ($LASTEXITCODE -ne 0) { Fail 'docker compose failed. See the messages above.' }

Write-Host -NoNewline 'Waiting for lanproxy to start'
$healthy = $false
for ($i = 0; $i -lt 60; $i++) {
    $status = docker inspect -f '{{.State.Health.Status}}' lanproxy 2>$null
    if ($status -eq 'healthy') { $healthy = $true; break }
    Write-Host -NoNewline '.'
    Start-Sleep -Seconds 2
}
Write-Host ''
if (-not $healthy) {
    Fail 'lanproxy did not become healthy. Check: docker compose logs'
}

Write-Host ''
Write-Host 'lanproxy is running. Open the web interface:' -ForegroundColor Green
Write-Host ''
Write-Host "  http://localhost:$port"
# Addresses of this PC in the local network (for other devices).
Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
    Where-Object { $_.PrefixOrigin -in 'Dhcp', 'Manual' -and $_.IPAddress -notlike '169.254.*' -and $_.InterfaceAlias -notmatch 'vEthernet|WSL|Loopback' } |
    ForEach-Object { Write-Host "  http://$($_.IPAddress):$port" }
Write-Host ''
Write-Host 'Note: the Windows firewall must allow incoming connections on ports 80, 443 and' $port
Write-Host 'if other devices in your network should reach lanproxy.'
Write-Host ''

Start-Process "http://localhost:$port"
