# Installing Kerneon

Kerneon is created and published by **Ryan Horth**.

## Requirements

- 64-bit Windows 10 or Windows 11.
- Administrator approval for the all-users MSI installation or removal.
- No account, background Windows service, bundled driver, or analytics client is installed.

## Install

Open `Kerneon-0.1.0-preview.1-x64.msi`, read and acknowledge the technical-preview notice, then choose **Install Kerneon**. The package installs to `C:\Program Files\Kerneon` and creates shared Start menu and public desktop shortcuts. Kerneon's optional vendor or frame-proof dependencies remain visible, separately consented actions inside the application; the MSI does not silently install them.

Only one Kerneon desktop instance runs in a Windows session. Starting Kerneon again restores the existing window.

## Upgrade and remove

A newer MSI upgrades the installed product while preserving Kerneon's per-user settings and diagnostic history. Remove Kerneon from **Settings > Apps > Installed apps**. Removal deletes the application, documentation, and installer-created shortcuts. It deliberately preserves `%APPDATA%\Kerneon` and `%LOCALAPPDATA%\Kerneon` so settings and locally held evidence are not destroyed without a separate user decision.

## Publisher trust

The technical-preview MSI carries Ryan Horth as its product manufacturer metadata but is not Authenticode-signed, so Windows can display an **Unknown publisher** or SmartScreen warning. Download only from `github.com/icydv/Kerneon`, verify `SHA256SUMS.txt`, and never disable Windows security tooling merely to run Kerneon. Code signing remains a future hardening goal rather than a claim made by this preview.

## Build the MSI

Run `scripts\build-msi.ps1` after producing `Kerneon-Surge-Advanced-Preview.exe`. The MSI is built with WiX Toolset 3.14, embeds its payload, generates artwork from Kerneon's canonical mark, and runs Windows Installer consistency validation before replacing the output package.
