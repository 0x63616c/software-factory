package main

import (
	"fmt"
	"os"
)

const buildVersion = "development"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(sfExitCode(err))
	}
}

func run() error {
	return newRootCommand().Execute()
}

func sfExitCode(err error) int {
	return sfExitCodeOrDefault(err, 1)
}
