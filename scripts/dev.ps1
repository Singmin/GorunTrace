param([switch]$Seed, [int]$Port = 8080)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $projectRoot
$goExecutable = Join-Path $projectRoot '.tools/go/bin/go.exe'
if (-not (Test-Path -LiteralPath $goExecutable)) {
    $goExecutable = (Get-Command go -ErrorAction Stop).Source
}
$env:GOPATH = Join-Path $projectRoot '.tools/gopath'
$env:GOCACHE = Join-Path $projectRoot '.tools/gocache'
$env:GOTELEMETRY = 'off'
$env:GOENV = 'off'
$env:PATH = (Split-Path -Parent $goExecutable) + ';' + $env:PATH
New-Item -ItemType Directory -Force bin,.tools | Out-Null
Push-Location web
try {
    if (-not (Test-Path node_modules)) { npm.cmd ci --cache ../.tools/npm-cache --no-audit --no-fund; if ($LASTEXITCODE -ne 0) { throw 'Dependency installation failed' } }
    npm.cmd run build
    if ($LASTEXITCODE -ne 0) { throw 'Dashboard build failed' }
} finally { Pop-Location }
& $goExecutable build -o bin/goruntrace.exe ./cmd/server
if ($LASTEXITCODE -ne 0) { throw 'Server build failed' }
$collectorProcess = Start-Process -FilePath (Join-Path $projectRoot 'bin/goruntrace.exe') -ArgumentList @('-addr',"127.0.0.1:$Port") -WorkingDirectory $projectRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput .tools/server.stdout.log -RedirectStandardError .tools/server.stderr.log
try {
    $ready = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        if ($collectorProcess.HasExited) { throw 'Collector exited; see .tools/server.stderr.log (the port may already be in use)' }
        try { $response = Invoke-RestMethod "http://127.0.0.1:$Port/healthz"; if ($response.status -eq 'ok') { $ready = $true; break } } catch { }
        Start-Sleep -Milliseconds 200
    }
    if (-not $ready) { throw 'Collector did not become ready' }
    if ($Seed) { & $goExecutable run ./examples/basic -endpoint "http://127.0.0.1:$Port"; if ($LASTEXITCODE -ne 0) { throw 'Example failed' } }
    Write-Host "GorunTrace: http://127.0.0.1:$Port — press Ctrl+C to stop."
    Wait-Process -Id $collectorProcess.Id
} finally {
    if (-not $collectorProcess.HasExited) { Stop-Process -Id $collectorProcess.Id }
}
