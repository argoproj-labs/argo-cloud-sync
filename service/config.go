package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v2"

	"github.com/argoproj-labs/argo-cloudops/internal/validations"
)

// CommandVariables respresents the config items for a command.
type CommandVariables struct {
	EnvironmentVariables string
	InitArguments        string
	ExecuteArguments     string
}

// Config represents the configuration.
type Config struct {
	Version   string
	Commands  map[string]map[string]string `yaml:"commands"`
	ImageURIs []string                     `yaml:"image_uris"`
}

func loadConfig(configFilePath string) (*Config, error) {
	f, err := ioutil.ReadFile(configFilePath)
	if err != nil {
		return nil, err
	}

	var config Config
	err = yaml.Unmarshal(f, &config)
	if err != nil {
		return nil, err
	}

	validations.SetImageURIs(config.ImageURIs)

	return &config, nil
}

func (c Config) getCommandDefinition(framework, commandType string) (string, error) {
	if _, ok := c.Commands[framework]; !ok {
		return "", fmt.Errorf("unknown framework '%s'", framework)
	}

	if _, ok := c.Commands[framework][commandType]; !ok {
		return "", fmt.Errorf("unknown command type '%s'", commandType)
	}

	return c.Commands[framework][commandType], nil
}

func (c Config) listFrameworks() []string {
	keys := []string{}
	for k := range c.Commands {
		keys = append(keys, k)
	}

	sort.Strings(keys)
	return keys
}

func (c Config) listTypes(framework string) ([]string, error) {
	if _, ok := c.Commands[framework]; !ok {
		return []string{}, fmt.Errorf("unknown framework '%s'", framework)
	}

	keys := []string{}
	for k := range c.Commands[framework] {
		keys = append(keys, k)
	}

	sort.Strings(keys)
	return keys, nil
}

func generateExecuteCommand(commandDefinition, environmentVariablesString string, arguments map[string][]string) (string, error) {
	initArguments := ""
	if _, ok := arguments["init"]; ok {
		initArguments = strings.Join(arguments["init"], " ")
	}

	executeArguments := ""
	if _, ok := arguments["execute"]; ok {
		executeArguments = strings.Join(arguments["execute"], " ")
	}

	commandVariables := CommandVariables{
		EnvironmentVariables: environmentVariablesString,
		InitArguments:        initArguments,
		ExecuteArguments:     executeArguments,
	}

	var buf bytes.Buffer
	t, err := template.New("text").Parse(commandDefinition)
	if err != nil {
		return "", err

	}
	err = t.Execute(&buf, commandVariables)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
