## Known limitations (read me first)

1. **Hot-reload does not re-fetch proxy-providers** (kernel limit): `PUT /configs` rebuilds provider objects but only reads the local cache, never re-fetching subscriptions; a no-restart re-fetch is `PUT /providers/proxies/{name}`. sub import/del involves adding/removing providers + rewiring groups, and mode rebuilds the firewall/tun/tproxy — all three take effect via a process restart
2. kill -9/OOM can leave firewall rules behind: `systemctl restart panoxy` cleans them automatically at startup, no manual work needed
3. **DoH (443) cannot be hijacked by the kernel**: browsers' built-in encrypted DNS bypasses routing split; status already warns about it — disabling it is recommended
4. The subscription prefetch is only a pre-validation; at runtime the kernel fetches remotely on its own interval
5. sub import `--name` depends on the config anchor `&p` (the base template ships it; fully hand-written configs must provide their own)
6. tun `stack: system` is the home default; for heavy BT / long UDP streaming / frequently dropping nodes / old kernels (5.4/5.15), switching to `gvisor` is recommended (a process crash gets revived by systemd — better than a silent hang)
7. **The binary is selected for the *build machine's* CPU**: the kernel is embedded in the CLI, and build.sh probes the local AVX2 support at compile time to pick GOAMD64 (present → v3, absent → v1); deploying across CPU classes (built on an AVX2 machine → run on an old non-AVX2 machine) will not start. For full compatibility build with `GOAMD64=v1 ./build.sh` (or compile on a machine without AVX2).
