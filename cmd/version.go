package cmd

import "runtime/debug"

// buildVersion is what --version prints: the version shells/install-docgen stamps from the
// newest docgen/vX.Y.Z tag, else what the Go toolchain recorded: the module version for
// `go install ...@version`, or the commit a plain `go build` was made from.
func buildVersion(stamped string) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	revision, dirty := "", ""
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if revision == "" {
		return "dev"
	}
	return "dev-" + revision[:min(7, len(revision))] + dirty
}
