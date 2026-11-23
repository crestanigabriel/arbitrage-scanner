// Command ingestor streams exchange order books and publishes them to NATS.
//
// This file is the service's composition root: concrete dependencies are
// constructed here and only here, then injected into the components that use them.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ingestor:", err)
		os.Exit(1)
	}
}

// run holds the program logic, taking its dependencies as parameters so it can be tested.
func run(out io.Writer) error {
	if _, err := fmt.Fprintln(out, "ingestor: hello"); err != nil {
		return fmt.Errorf("write greeting: %w", err)
	}
	return nil
}
