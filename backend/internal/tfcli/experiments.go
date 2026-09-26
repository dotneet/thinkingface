package tfcli

import (
	"fmt"
	"io"
)

// experimentsUsage is the help text for `tf experiments`
// (docs/dev/agent-features.md §4).
const experimentsUsage = `Usage:
  tf experiments <subcommand> [flags] [args]

Subcommands:
  runs      List a project's runs as a table (filter / sort server-side)
  run       Show one run
  wait      Wait until a run satisfies a condition
  diff      Show the config keys that differ between runs
  goals     Show or set which direction each metric improves in
  notes     Show or replace a project's experiment notes
  annotate  Set a run's note, tags or archived flag
  import    Import past runs from CSV / JSONL files
  sync      Upload runs recorded offline by thinkingface.trackio

REPO is ns/name (the dataset repository the runs are logged to). Every
subcommand takes --json for machine-readable output on stdout, plus the
common --endpoint / --token / --api-key / --verbose flags.

Run 'tf experiments help <subcommand>' or 'tf experiments <subcommand>
--help' for its flags.
`

// runExperiments dispatches `tf experiments <subcommand>`.
func runExperiments(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, experimentsUsage)
		return exitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--help":
		fmt.Fprint(stdout, experimentsUsage)
		return exitOK
	case "help":
		// `tf experiments help <sub>` is `tf experiments <sub> --help`.
		if len(rest) > 0 {
			if usage, ok := experimentsSubUsage(rest[0]); ok {
				fmt.Fprint(stdout, usage)
				return exitOK
			}
			if rest[0] == "import" || rest[0] == "sync" {
				return runExperiments([]string{rest[0], "--help"}, stdin, stdout, stderr)
			}
			fmt.Fprintf(stderr, "tf experiments: unknown subcommand %q\n", rest[0])
			fmt.Fprint(stderr, experimentsUsage)
			return exitUsage
		}
		fmt.Fprint(stdout, experimentsUsage)
		return exitOK
	case "import":
		return runExperimentsImport(rest, stdin, stdout, stderr)
	case "sync":
		return runExperimentsSync(rest, stdin, stdout, stderr)
	default:
		return runExperimentsQuery(sub, rest, stdin, stdout, stderr)
	}
}
