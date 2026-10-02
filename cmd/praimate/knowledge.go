package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/knowledge"
)

func runKnowledge(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: praimate knowledge index|query|graph --root DIRECTORY [--question TEXT --budget 1200]")
		return 2
	}
	action := args[0]
	f := flag.NewFlagSet("knowledge "+action, flag.ContinueOnError)
	root := f.String("root", "", "knowledge document directory")
	question := f.String("question", "", "focused retrieval question")
	budget := f.Int("budget", 1200, "approximate output token budget (200–8000)")
	if err := f.Parse(args[1:]); err != nil {
		return 2
	}
	if *root == "" || f.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--root is required; unexpected positional arguments are not accepted")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var err error
	switch action {
	case "index":
		var idx *knowledge.Index
		idx, err = knowledge.Build(ctx, *root)
		if err == nil {
			err = knowledge.SetEngine(*root, "native")
		}
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(map[string]any{"documents": len(idx.Documents), "chunks": len(idx.Chunks), "nodes": len(idx.Graph.Nodes), "skipped": idx.Skipped})
		}
	case "query":
		var answer string
		answer, err = knowledge.Query(ctx, *root, *question, *budget)
		if err == nil {
			fmt.Fprintln(os.Stdout, answer)
		}
	case "graph":
		var idx *knowledge.Index
		idx, err = knowledge.Ensure(ctx, *root)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(idx.Graph)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown knowledge action:", action)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func runAgentConvert(args []string) int {
	f := flag.NewFlagSet("agent convert", flag.ContinueOnError)
	input := f.String("input", "", "source .md/.yaml file")
	output := f.String("output", "", "destination .md/.yaml file")
	if err := f.Parse(args); err != nil {
		return 2
	}
	if *input == "" || *output == "" || f.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: praimate agent convert --input SOURCE --output DESTINATION")
		return 2
	}
	agent, err := core.LoadAgentFile(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var raw []byte
	switch strings.ToLower(filepath.Ext(*output)) {
	case ".md", ".markdown":
		raw, err = core.MarshalAgentMarkdown(agent)
	case ".yaml", ".yml":
		raw, err = core.MarshalAgentYAML(agent)
	default:
		fmt.Fprintln(os.Stderr, "destination must use .md, .markdown, .yaml or .yml")
		return 2
	}
	if err == nil { // Parse the generated document before creating an output file.
		if strings.HasSuffix(strings.ToLower(*output), ".yaml") || strings.HasSuffix(strings.ToLower(*output), ".yml") {
			_, err = core.ParseAgentYAML(bytes.NewReader(raw))
		} else {
			_, err = core.ParseAgentMarkdown(bytes.NewReader(raw), *output)
		}
	}
	if err == nil {
		var file *os.File
		file, err = os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, err = file.Write(raw)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "Converted agent:", *output)
	if len(agent.Foreign) > 0 {
		fmt.Fprintln(os.Stderr, "External model/tool/permission options are preserved for export; PrAImate uses its own runtime settings.")
	}
	return 0
}
