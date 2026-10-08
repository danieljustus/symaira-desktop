// Captures native Windows DefaultPath observations from the pinned Go loader.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"

	"github.com/danieljustus/symaira-corekit/configkit"
)

type pathCase struct {
	ID       string `json:"id"`
	Input    string `json:"input"`
	Expected string `json:"expected"`
}

type capture struct {
	GoOS      string     `json:"goos"`
	GoArch    string     `json:"goarch"`
	GoVersion string     `json:"go_version"`
	Cases     []pathCase `json:"cases"`
}

func main() {
	check := flag.String("check", "", "compare native Go cases without rewriting the recorded capture")
	flag.Parse()
	if flag.NArg() != 0 || runtime.GOOS != "windows" || runtime.Version() != "go1.26.9" {
		fail("native Windows and Go 1.26.9 required")
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fail("missing Go build identity")
	}
	pinned := false
	for _, dependency := range info.Deps {
		if dependency.Path == "github.com/danieljustus/symaira-corekit" {
			pinned = dependency.Version == "v0.18.2" && dependency.Sum == "h1:Nl05PxfSrYJ5njEbdmqFHSQRtKlK6aj8HsOJReC15dY=" && dependency.Replace == nil
		}
	}
	if !pinned {
		fail("CoreKit v0.18.2 source identity mismatch")
	}
	cases := []pathCase{
		{ID: "drive-canonical", Input: `\\?\C:\root`},
		{ID: "drive-mixed-separators", Input: `\\?\C:/root`},
		{ID: "drive-dot-and-parent", Input: `\\?\C:\root\.\nested\..\leaf`},
		{ID: "drive-parent", Input: `\\?\C:\root\..\leaf`},
		{ID: "drive-parent-at-root", Input: `\\?\C:\..\leaf`},
		{ID: "unc-canonical", Input: `\\?\UNC\server\share\root`},
		{ID: "unc-mixed-separators", Input: `\\?\UNC\server\share/root`},
		{ID: "unc-parent", Input: `\\?\UNC\server\share\root\..\leaf`},
		{ID: "unc-parent-at-share", Input: `\\?\UNC\server\share\..\leaf`},
		{ID: "drive-slash-namespace", Input: `//?/C:/root`},
		{ID: "unc-slash-namespace", Input: `//?/UNC/server/share/root`},
	}
	for i := range cases {
		if err := os.Setenv("XDG_CONFIG_HOME", cases[i].Input); err != nil {
			fail(err.Error())
		}
		cases[i].Expected = configkit.DefaultPath("symdesk")
	}
	observed := capture{GoOS: runtime.GOOS, GoArch: runtime.GOARCH, GoVersion: runtime.Version(), Cases: cases}
	if *check != "" {
		raw, err := os.ReadFile(*check)
		if err != nil {
			fail(err.Error())
		}
		var recorded capture
		if err := json.Unmarshal(raw, &recorded); err != nil {
			fail(err.Error())
		}
		// Preserve the original native Go 1.26.6 capture metadata while replaying
		// its path cases under the active Go 1.26.9 toolchain.
		if recorded.GoOS != observed.GoOS || recorded.GoVersion != "go1.26.6" {
			fail("recorded capture identity is not the preserved Go 1.26.6 record")
		}
		// The recorded architecture is retained; path observations are portable
		// across Windows architectures and compared against this native replay.
		if (recorded.GoArch != "amd64" && recorded.GoArch != "arm64") || !reflect.DeepEqual(recorded.Cases, observed.Cases) {
			fail("recorded cases differ from actual native Go observations")
		}
		fmt.Fprintf(os.Stderr, "Native Go path capture check passed: %d cases on %s\n", len(cases), runtime.GOARCH)
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(observed); err != nil {
		fail(err.Error())
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
