// Command docgen extracts documentation facts from a repository, checks docs against
// them, and (with a local model) drafts documentation from them.
package main

import (
	"os"

	"github.com/JB0t/docgen/cmd"
)

func main() { os.Exit(cmd.Execute()) }
