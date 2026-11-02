package main

import (
	"bufio"
	"fmt"
	"os"

	"copper_bmc/internal/app"
	"copper_bmc/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Prompt(bufio.NewReader(os.Stdin))
	if err != nil {
		return err
	}
	return app.Run(cfg)
}
