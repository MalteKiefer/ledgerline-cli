module github.com/MalteKiefer/ledgerline-cli

go 1.25.0

// Pin the build toolchain to the first release that fixes GO-2026-5856
// (crypto/tls Encrypted Client Hello privacy leak). Builds with an older
// toolchain automatically fetch this one.
toolchain go1.26.5

require (
	github.com/fsnotify/fsnotify v1.9.0
	github.com/spf13/cobra v1.10.2
	github.com/zalando/go-keyring v0.2.8
)

require (
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
