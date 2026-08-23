package tfcli

import (
	"fmt"
	"io"
	"runtime"
)

const versionUsage = `usage: tf version

Print the tf version, Go OS and architecture.
`

func runVersion(args []string, stdout, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, versionUsage)
		return exitOK
	}
	if len(args) > 0 {
		fmt.Fprintln(stderr, "tf: version takes no arguments")
		fmt.Fprint(stderr, versionUsage)
		return exitUsage
	}
	fmt.Fprintf(stdout, "tf %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	return exitOK
}
