package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/elysola/mailer/internal/processors/wasm"
)

// RunPlugins implements plugin inspection commands without starting the daemon.
func RunPlugins(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mailer plugins {list|validate|test} [flags]")
		return 2
	}

	switch args[0] {
	case "list", "validate":
		fs := flag.NewFlagSet("plugins "+args[0], flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		dir := fs.String("dir", "./plugins", "plugin directory")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		engine, err := wasm.LoadDir(*dir, wasm.Limits{
			Timeout:   100 * time.Millisecond,
			MaxInput:  512 * 1024,
			MaxOutput: 16 * 1024,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "plugins: %v\n", err)
			return 1
		}
		if args[0] == "validate" {
			fmt.Printf("validated %d plugin(s)\n", len(engine.Names()))
			return 0
		}
		for i, name := range engine.Names() {
			fmt.Printf("%d. %s\n", i+1, name)
		}
		return 0

	case "test":
		fs := flag.NewFlagSet("plugins test", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		dir := fs.String("dir", "./plugins", "plugin directory")
		inputPath := fs.String("input", "", "JSON file containing a wasm.Mail value")
		if err := fs.Parse(args[1:]); err != nil || *inputPath == "" {
			if *inputPath == "" {
				fmt.Fprintln(os.Stderr, "plugins test: -input is required")
			}
			return 2
		}
		data, err := os.ReadFile(*inputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "input: %v\n", err)
			return 1
		}
		var msg wasm.Mail
		if err := json.Unmarshal(data, &msg); err != nil {
			fmt.Fprintf(os.Stderr, "input JSON: %v\n", err)
			return 1
		}
		engine, err := wasm.LoadDir(*dir, wasm.Limits{
			Timeout:   100 * time.Millisecond,
			MaxInput:  512 * 1024,
			MaxOutput: 16 * 1024,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "plugins: %v\n", err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := engine.Process(ctx, msg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plugins test: %v\n", err)
			return 1
		}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "result: %v\n", err)
			return 1
		}
		fmt.Println(string(encoded))
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown plugins command %q\n", args[0])
		return 2
	}
}
