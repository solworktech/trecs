package lib

import "runtime/debug"

// Version is the version of this build. A release sets it when linking, to the tag it was built from:
//
//	go build -ldflags "-X trecs/lib.Version=v1.2.3" ./recorder
//
// Left empty (a build from a clone), VersionString says which commit it was built from.
var Version string

// VersionString is the version to show for --version: the release's tag; otherwise the module's version (`go install
// trecs/recorder@v1.2.3`); otherwise the commit of the clone it was built from, with "-dirty" if it had uncommitted changes.
func VersionString() string {
	bi, ok := debug.ReadBuildInfo()
	return versionFrom(Version, bi, ok)
}

func versionFrom(linked string, bi *debug.BuildInfo, ok bool) string {
	if linked != "" {
		return linked
	}
	if !ok || bi == nil {
		return "unknown"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev string
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "devel" // not built in a clone (a source archive, or `go run`)
	}
	rev = rev[:min(len(rev), 12)]
	if dirty {
		rev += "-dirty"
	}
	return rev
}
