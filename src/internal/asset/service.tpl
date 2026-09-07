[Unit]
Description={{.Prog}} mihomo transparent proxy ({{.Mode}})
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
# With a custom install dir, the fw apply/upgrade subprocesses find the state and data via this (matches --root)
Environment={{.EnvPrefix}}_ROOT={{.Root}}
# Pre-start config validation (in-process -t)
ExecStartPre={{.Cli}} check
# Run the kernel in-process (mihomo Go code fused in; no external binary started)
ExecStart={{.Cli}} run
# DNS hijack rules: apply unconditionally CleanAll's before loading — kill -9/OOM leftovers self-heal on restart
ExecStartPost={{.Cli}} fw apply
ExecStop={{.Cli}} fw clean
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
