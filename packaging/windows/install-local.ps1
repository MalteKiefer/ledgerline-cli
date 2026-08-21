# Replace the installed tray and CLI with the ones in ./bin.
#
# For testing a change on the machine that built it without stepping through the
# installer. Program Files needs an administrator, so the copy runs elevated; the
# tray is stopped first because an open handle on the .exe blocks it.
#
# Run it through `make install-windows-local`, or directly:
#   powershell -ExecutionPolicy Bypass -File packaging\windows\install-local.ps1

$ErrorActionPreference = 'Stop'

$src = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'bin'
$dst = Join-Path $env:ProgramFiles 'Ledgerline'
$gui = Join-Path $src 'ledgerline-gui.exe'
$cli = Join-Path $src 'ledgerline.exe'

if (-not (Test-Path $gui)) { throw "build $gui first" }

# One elevated shell for both copies: one consent prompt, not two.
$copy = "taskkill /IM ledgerline-gui.exe /F >nul 2>&1 & xcopy `"$gui`" `"$dst\`" /Y /Q"
if (Test-Path $cli) { $copy += " & xcopy `"$cli`" `"$dst\`" /Y /Q" }

Start-Process cmd.exe -ArgumentList '/c', $copy -Verb RunAs -Wait
Start-Process (Join-Path $dst 'ledgerline-gui.exe')
Write-Host "installed to $dst and restarted the tray"
