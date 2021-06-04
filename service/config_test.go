package main

import (
	"testing"

	"github.com/argoproj-labs/argo-cloudops/internal/env"
)

func TestGenerateExecuteCommand(t *testing.T) {
	arguments := map[string][]string{}
	arguments["init"] = []string{"--initialize", "--debug"}
	arguments["execute"] = []string{"--go"}

	testEnv := env.EnvVars{
		ConfigFilePath: "../service/testdata/argo-cloudops.yaml",
	}
	config, err := loadConfig(testEnv)
	if err != nil {
		t.Errorf("Unable to load config %s", err)
	}

	// test sync
	commandDefinition, err := config.getCommandDefinition("cool-new-framework", "sync")
	if err != nil {
		t.Errorf("get command definition return error %s", err)
	}
	result, err := generateExecuteCommand(commandDefinition, "env test=abc", arguments)
	if err != nil {
		t.Errorf("generateExecuteCommand return error %s", err)
	}
	expect := "env test=abc fire --initialize --debug && env test=abc ready-aim --go"
	if result != expect {
		t.Errorf("generateExecuteCommand expected '%s' got '%s'", expect, result)
	}

	// test diff
	commandDefinition, err = config.getCommandDefinition("cool-new-framework", "diff")
	if err != nil {
		t.Errorf("get command definition return error %s", err)
	}
	result, err = generateExecuteCommand(commandDefinition, "env test=abc", arguments)
	if err != nil {
		t.Errorf("generateExecuteCommand return error %s", err)
	}
	expect = "env test=abc get-ready --initialize --debug && env test=abc diffit --go"
	if result != expect {
		t.Errorf("generateExecuteCommand expected '%s' got '%s'", expect, result)
	}
}

func TestGetCommandDefinition(t *testing.T) {
	testEnv := env.EnvVars{
		ConfigFilePath: "../service/testdata/argo-cloudops.yaml",
	}
	config, err := loadConfig(testEnv)
	if err != nil {
		t.Errorf("Unable to load config %s", err)
	}

	// unknown framework
	_, err = config.getCommandDefinition("not-so-cool-new-framework", "sync")
	if err.Error() != "Unknown framework 'not-so-cool-new-framework'" {
		t.Errorf("expected error for unknown framework")
	}

	// unknown type
	_, err = config.getCommandDefinition("cool-new-framework", "razzle-dazzle")
	if err.Error() != "Unknown command type 'razzle-dazzle'" {
		t.Errorf("expected error for unknown type")
	}
}
