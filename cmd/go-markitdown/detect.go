package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/roelfdiedericks/go-markitdown/docconv"
)

func newDetectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detect <input|->",
		Short: "Print the detected format of a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDetect(args[0])
		},
	}
}

func runDetect(input string) error {
	if input == "-" {
		format, _, err := docconv.DetectReader(os.Stdin)
		if err != nil {
			return err
		}
		fmt.Println(format.String())
		return nil
	}
	if _, err := os.Stat(input); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return badArgsError(fmt.Errorf("input not found: %s", input))
		}
		return err
	}
	format, err := docconv.Detect(input)
	if err != nil {
		return err
	}
	fmt.Println(format.String())
	return nil
}
