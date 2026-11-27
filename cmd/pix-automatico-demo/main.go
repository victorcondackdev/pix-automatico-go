// Command pix-automatico-demo runs the bundled scenarios and prints their
// transcripts: every command, its outcome and the events it caused.
//
//	go run ./cmd/pix-automatico-demo            # all scenarios
//	go run ./cmd/pix-automatico-demo energia    # one
//	go run ./cmd/pix-automatico-demo -f my.txt  # your own script
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/victorcondackdev/pix-automatico-go/scenario"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pix-automatico-demo:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pix-automatico-demo", flag.ContinueOnError)
	file := fs.String("f", "", "run a script file instead of the bundled examples")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file != "" {
		src, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		out, err := scenario.Run(string(src))
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	names := fs.Args()
	if len(names) == 0 {
		names = scenario.ExampleNames()
	}
	ex := scenario.Examples()
	for i, n := range names {
		src, ok := ex[n]
		if !ok {
			return fmt.Errorf("cenário %q não existe (disponíveis: %v)", n, scenario.ExampleNames())
		}
		out, err := scenario.Run(src)
		if err != nil {
			return err
		}
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("=== %s ===\n%s", n, out)
	}
	return nil
}
