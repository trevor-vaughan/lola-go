module github.com/trevor-vaughan/lola-go

go 1.26.0

toolchain go1.26.11

require (
	charm.land/bubbles/v2 v2.2.2
	charm.land/bubbletea/v2 v2.2.0
	charm.land/huh/v2 v2.0.3
	charm.land/lipgloss/v2 v2.1.1
	github.com/BurntSushi/toml v1.6.0
	github.com/charmbracelet/x/ansi v0.12.0
	github.com/gofrs/flock v0.13.2
	github.com/onsi/ginkgo/v2 v2.34.0
	github.com/onsi/gomega v1.44.0
	github.com/osteele/liquid v1.9.2
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10 // pinned: v1.1.0 shows bool flags as --name[=true|false] in help, no opt-out (spf13/pflag#454)
	github.com/tailscale/hujson v0.0.0-20260727124030-b80ff77dac4f
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/mod v0.43.0
	golang.org/x/term v0.48.0
)

require (
	github.com/Masterminds/semver/v3 v3.5.0 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/catppuccin/go v0.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20261125122737-8ed499892fb4 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/exp/strings v0.1.0 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/pprof v0.0.0-20261118010117-084b33a85f63 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-runewidth v0.0.31 // indirect
	github.com/mitchellh/hashstructure/v2 v2.0.2 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/osteele/tuesday v1.1.1 // indirect
	github.com/rivo/uniseg v0.4.8-0.20261110175422-3a370411ca06 // indirect
	github.com/xo/terminfo v1.3.1 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/tools v0.53.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)

// liquid v1.9.2 with one patch to render/trimwriter.go; see third_party/liquid/PATCHES.md.
replace github.com/osteele/liquid => ./third_party/liquid
