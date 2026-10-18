package lib

import (
	"runtime/debug"
	"testing"
)

func TestVersionIsTheTagLinkedInOrElseTheCommit(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef0123"}, {Key: "vcs.modified", Value: "false"},
	}}
	dirty := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef0123"}, {Key: "vcs.modified", Value: "true"},
	}}
	for _, c := range []struct {
		name, linked string
		bi           *debug.BuildInfo
		ok           bool
		want         string
	}{
		{"a release is its tag", "v1.2.3", bi, true, "v1.2.3"},
		{"a clone is its commit", "", bi, true, "0123456789ab"},
		{"uncommitted changes are said", "", dirty, true, "0123456789ab-dirty"},
		{"go install pkg@version is the module's", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}}, true, "v0.9.0"},
		{"no vcs information", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true, "devel"},
		{"no build information", "", nil, false, "unknown"},
	} {
		if got := versionFrom(c.linked, c.bi, c.ok); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
