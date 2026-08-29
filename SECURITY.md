# Security Policy

power-bridge runs unattended on internet-connected Raspberry Pi devices with
root privileges, so we treat security reports seriously and appreciate
responsible disclosure.

## Reporting a vulnerability

Please report suspected vulnerabilities privately using
[GitHub's private vulnerability reporting](https://github.com/agento07hdm/power-bridge/security/advisories/new)
("Security" tab → "Report a vulnerability") rather than filing a public issue.

Please include:

- A description of the issue and its potential impact
- Steps to reproduce, or a proof of concept if available
- The affected version(s)

We aim to acknowledge reports within a few days and to ship a fix (or a
mitigation) before any public disclosure.

## Supported versions

Only the latest released version is supported. Please update before
reporting an issue that may already be fixed — see the "Jetzt aktualisieren"
button in the device's web UI, or `README.md` for manual update instructions.

## Update & release integrity

- Releases are built by GitHub Actions from tagged source and published with
  a [SHA256SUMS](https://github.com/agento07hdm/power-bridge/releases) file
  and a [GitHub build provenance attestation](https://github.com/agento07hdm/power-bridge/attestations),
  so a release asset can't be silently swapped after the fact without
  detection.
- Devices install updates manually (via the web UI or `update.sh apply`),
  not automatically at every boot — see the "Automatische OTA-Updates"
  section in `README.md`.
