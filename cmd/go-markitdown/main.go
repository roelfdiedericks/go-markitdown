// Command go-markitdown converts common document formats into LLM-ready
// markdown. It is a thin wrapper around the docconv library.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Exit codes per SPEC.
const (
	exitOK            = 0
	exitExtractionErr = 1
	exitUnsupported   = 2
	exitBadArgs       = 3
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "go-markitdown",
		Short:         "Convert documents (PDF, DOCX, XLSX, PPTX, EPUB, MOBI, HTML, text) to markdown",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newConvertCmd())
	root.AddCommand(newDetectCmd())
	root.AddCommand(newVersionCmd())
	return root
}

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "go-markitdown: %s\n", err)
		os.Exit(codeForError(err))
	}
}
