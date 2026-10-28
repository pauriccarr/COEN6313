// Command faulttest is the checkout agent's fault injection test runner: the
// Go counterpart of test_fault_injection.py in the other agents. It runs every
// fault mode (or the ones named) against the mock backend, so no cluster,
// gRPC services or Ollama are needed, prints a report and saves the JSON.
//
// Usage:
//
//	go run ./cmd/faulttest
//	go run ./cmd/faulttest FM_2_5 BL_WRONG_TOTAL
//	RESULTS_DIR=./results go run ./cmd/faulttest -currency USD
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"checkoutservice-agent/faultlab"
)

func main() {
	currency := flag.String("currency", "EUR", "user currency of the test order")
	verbose := flag.Bool("v", false, "keep the agent's own output")
	flag.Parse()

	modes := faultlab.FilterModes(flag.Args())
	if len(modes) == 0 {
		fmt.Fprintln(os.Stderr, "no known fault modes given")
		os.Exit(2)
	}
	dir := os.Getenv("RESULTS_DIR")
	if dir == "" {
		dir = "/results"
	}

	mock, err := faultlab.StartMock()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer mock.Close()
	r, err := mock.Runner(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	r.Currency = *currency
	r.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

	// The Ollama client prints debug lines to stdout; keep the report readable.
	stdout := os.Stdout
	if !*verbose {
		if null, err := os.Open(os.DevNull); err == nil {
			os.Stdout = null
		}
	}
	fmt.Fprintf(os.Stderr, "\nRunning %d fault modes (mock backend, %s)...\n", len(modes)+1, *currency)
	res := r.RunAll(modes)
	os.Stdout = stdout

	faultlab.PrintReport(os.Stdout, res)
	if err := faultlab.Save(dir, res); err != nil {
		fmt.Fprintf(os.Stderr, "could not save results: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Results saved to: %s\n\n", filepath.Join(dir, faultlab.ResultsFile))
	for _, row := range res.Results {
		if row.Error != "" {
			os.Exit(1)
		}
	}
}
