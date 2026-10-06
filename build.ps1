<#
.SYNOPSIS
Builds flacsync.exe and takes care of the Windows-specific annoyances.

.DESCRIPTION
A running flacsync.exe locks its own file, so an ordinary `go build` cannot
replace it. This script stops (with -Force) or refuses to build over a running
instance, regenerates the embedded icon when logo.ico changed, and can run
vet/tests before or launch the result after the build.

.PARAMETER Out
Output path. Defaults to flacsync.exe in the repository root. Point it at
another file to build a new version while the old one keeps running.

.PARAMETER Console
Keep a console window in the build (omit -H=windowsgui), useful when running
with -v to watch events and jobs.

.PARAMETER Release
Add -trimpath -s -w to the build, matching the artifacts published by CI.

.PARAMETER Test
Run `go vet ./...` and `go test ./...` first and abort if either fails.

.PARAMETER Force
Terminate a running flacsync.exe instead of asking the user to quit it.

.PARAMETER Run
Start the freshly built executable when the build succeeds.

.EXAMPLE
.\build.ps1
Builds the console-less tray executable.

.EXAMPLE
.\build.ps1 -Test -Release -Run
Verify, build an optimized executable, then launch it.
#>
[CmdletBinding()]
param(
    [string]$Out = 'flacsync.exe',
    [switch]$Console,
    [switch]$Release,
    [switch]$Test,
    [switch]$Force,
    [switch]$Run
)

$ErrorActionPreference = 'Stop'

$root = $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }
if (-not [System.IO.Path]::IsPathRooted($Out)) { $Out = Join-Path $root $Out }
$exe = [System.IO.Path]::GetFullPath($Out)

function Fail([string]$message) {
    Write-Host $message -ForegroundColor Red
    exit 1
}

function Find-Instances {
    $name = [System.IO.Path]::GetFileName($exe)
    @(Get-CimInstance Win32_Process -Filter "Name = '$name'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and ($_.ExecutablePath -ieq $exe) })
}

# A running instance holds a lock on the file, so the build must either stop
# it or give up before touching anything. @() matters: PowerShell unrolls a
# one-element array, and a bare CimInstance has no usable .Count.
$running = @(Find-Instances)
if ($running.Count -gt 0) {
    if (-not $Force) {
        $pids = ($running | ForEach-Object { $_.ProcessId }) -join ', '
        Fail "flacsync is running (PID $pids). Quit it from the tray, or rerun with -Force to terminate it."
    }
    foreach ($p in $running) {
        Write-Host "Stopping running flacsync (PID $($p.ProcessId)) ..."
        Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
    }
    for ($i = 0; $i -lt 25; $i++) {
        Start-Sleep -Milliseconds 200
        if (@(Find-Instances).Count -eq 0) { break }
    }
}

# The .syso is the compiled Windows resource. Rebuild it only when the source
# icon is newer, so the common case needs no windres at all.
$ico = Join-Path $root 'internal\icon\logo.ico'
$syso = Join-Path $root 'flacsync_windows_amd64.syso'
$needIcon = (Test-Path $ico) -and
    ((Test-Path $syso) -eq $false -or (Get-Item $ico).LastWriteTimeUtc -gt (Get-Item $syso).LastWriteTimeUtc)
if ($needIcon) {
    if (-not (Get-Command windres -ErrorAction SilentlyContinue)) {
        Fail "internal\icon\logo.ico is newer than flacsync_windows_amd64.syso, but windres was not found on PATH. Install mingw-w64, or restore the .syso from git."
    }
    Write-Host 'Regenerating flacsync_windows_amd64.syso from logo.ico ...'
    & windres '--preprocessor=gcc -E -xc -DRC_INVOKED' (Join-Path $root 'app.rc') -O coff -o $syso
    if ($LASTEXITCODE -ne 0) {
        Fail 'windres failed; fix the icon or restore flacsync_windows_amd64.syso from git.'
    }
}

$goArgs = @('build')
$ld = @()
if (-not $Console) { $ld += '-H=windowsgui' }
if ($Release) {
    $goArgs += '-trimpath'
    $ld += '-s'
    $ld += '-w'
}
if ($ld.Count -gt 0) { $goArgs += @('-ldflags', ($ld -join ' ')) }
$goArgs += @('-o', $exe, '.')

$failed = $false
Push-Location $root
try {
    if ($Test) {
        Write-Host 'go vet ./...'
        & go vet ./...
        if ($LASTEXITCODE -ne 0) { $failed = $true }

        if (-not $failed) {
            Write-Host 'go test ./...'
            & go test ./...
            if ($LASTEXITCODE -ne 0) { $failed = $true }
        }
    }

    if (-not $failed) {
        # Remove first for the same reason the script exists: a stale or
        # partially locked exe should surface here, with a clear error.
        if (Test-Path $exe) {
            try {
                Remove-Item -Force $exe -ErrorAction Stop
            } catch {
                throw "cannot replace $([System.IO.Path]::GetFileName($exe)): $($_.Exception.Message) (quit flacsync from the tray, or rerun with -Force)"
            }
        }
        Write-Host ('go ' + ($goArgs -join ' '))
        & go @goArgs
        if ($LASTEXITCODE -ne 0) { $failed = $true }
    }
} catch {
    Write-Host "Build failed: $_" -ForegroundColor Red
    $failed = $true
} finally {
    Pop-Location
}

if ($failed) { exit 1 }

$info = Get-Item $exe
$kind = 'tray, no console'
if ($Console) { $kind = 'console' }
$profile = 'debug'
if ($Release) { $profile = 'release' }
Write-Host ("Built {0} ({1:N0} bytes, {2}, {3})" -f $info.FullName, $info.Length, $kind, $profile) -ForegroundColor Green

if ($Run) {
    Write-Host 'Starting flacsync ...'
    Start-Process -FilePath $exe
}
