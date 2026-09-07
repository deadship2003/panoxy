## Troubleshooting

- `status` shows 0 nodes: the subscription did not load → re-run `sub import` (`--file` works offline); if it still fails, check `panoxy log`
- Traffic cut off: first try `sudo systemctl restart panoxy` (the firewall self-heals); if it persists, `panoxy mode` to confirm the mode and `--trace` to watch rule loading
- Broken config: `panoxy check` + the kernel's first error is passed through as a `level=error msg` line
- UI upgrade abnormal: the old panel is rolled back automatically; a very old `.last-upgrade` means upgrades are stuck — check `panoxy log`
