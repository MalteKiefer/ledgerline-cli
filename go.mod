module github.com/MalteKiefer/ledgerline-cli

go 1.25.0

// Pin the build toolchain to the first release that fixes GO-2026-6218
// (net/url quadratic-complexity resolvePath), GO-2026-6090 (crypto/tls
// post-handshake message flood), GO-2026-5972 (encoding/asn1 unbounded
// recursion) and GO-2026-5026 (net/http/x/net/idna Punycode ASCII-only
// label bypass). Builds with an older toolchain automatically fetch this one.
toolchain go1.26.6

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
