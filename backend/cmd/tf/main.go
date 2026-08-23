// Command tf is the thinkingface command-line client: one command to register
// a dataset or model (`tf up ./dir`) plus the login that makes it possible.
//
// It speaks only the HuggingFace-compatible HTTP API the server already
// exposes (whoami / create / preupload / LFS batch / commit), so everything it
// does could be done with `hf upload` as well -- it just removes the
// endpoint/token/repo-type ceremony (docs/tf-cli.md).
package main

import (
	"os"

	"github.com/dotneet/thinkingface/backend/internal/tfcli"
)

func main() {
	os.Exit(tfcli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
