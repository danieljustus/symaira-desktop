package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestDatasetCLICommandsRegistered(t *testing.T) {
	root := newRootCmd()
	var datasetCmd *cobra.Command
	for _, cmd := range root.Commands() {
		if cmd.Name() == "dataset" {
			datasetCmd = cmd
			break
		}
	}
	if datasetCmd == nil {
		t.Fatal("dataset command is not registered")
	}
	want := map[string]bool{"list": true, "describe": true, "query": true, "sync": true}
	for _, cmd := range datasetCmd.Commands() {
		delete(want, cmd.Name())
	}
	if len(want) != 0 {
		t.Fatalf("dataset subcommands missing: %#v", want)
	}
}
