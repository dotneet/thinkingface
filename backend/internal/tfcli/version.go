package tfcli

import (
	"fmt"
	"io"
	"runtime"
)

const versionUsage = `usage: tf version [--json]

Print the tf version, Go OS and architecture.

Flags:
  --json                 print {"version", "os", "arch", "go_version"} on stdout
`

// versionJSON is the shape written by `tf version --json`.
type versionJSON struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, versionUsage)
		return exitOK
	}
	jsonOut := false
	for _, a := range args {
		if a == "--json" || a == "-json" {
			jsonOut = true
			continue
		}
		fmt.Fprintln(stderr, "tf: version takes no arguments")
		fmt.Fprint(stderr, versionUsage)
		return exitUsage
	}
	if jsonOut {
		return writeJSONLine(stdout, stderr, &versionJSON{
			Version: Version, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(),
		})
	}
	fmt.Fprintf(stdout, "tf %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	return exitOK
}
