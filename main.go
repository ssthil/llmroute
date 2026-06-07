// Command llmroute is a native, multi-LLM routing proxy CLI.
package main

import "github.com/ssthil/llmroute/internal/cli"

// version is injected at build time via -ldflags
// "-X main.version=$(VERSION)".
var version = "dev"

func main() {
	cli.SetVersion(version)
	cli.Execute()
}
