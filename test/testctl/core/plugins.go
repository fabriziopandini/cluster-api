package core

import (
	"context"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// CallStackValidatorPlugin defines a plugin that validates its call stack.
type CallStackValidatorPlugin interface {
	// ValidateCallStack validate the call stack for the plugin.
	ValidateCallStack(ctx context.Context, callStack []string) error
}

// MessageGeneratorPlugin defines a plugin that generates a prompt message.
type MessageGeneratorPlugin interface {
	// GenerateMessage generate a message text for the plugin call.
	GenerateMessage(objects TestObjects, pluginConfig any) (string, error)
}

// SelectorPlugin defines a test runner plugin that selects a set of objects to act on during the test.
type SelectorPlugin interface {
	// Selects a set of objects to act on during the test.
	Select(ctx context.Context, c client.Client, objects TestObjects, pluginConfig any, runConfig RunConfig) (testObjectsList []TestObjects, err error)
}

// ExecutorPlugin defines a test runner plugin that executes an action on the given objects during the test.
type ExecutorPlugin interface {
	// Exec execute an action on the given objects during the test.
	Exec(ctx context.Context, c client.Client, objects TestObjects, pluginConfig any, runConfig RunConfig) error
}

// rootSelectorPlugin is an internal plugin that is used only to kick off the run process.
type rootSelectorPlugin struct{}

var _ SelectorPlugin = &rootSelectorPlugin{}

// Select returns a list of Objects with only one item that represents the root context of the test.
func (r *rootSelectorPlugin) Select(_ context.Context, _ client.Client, _ TestObjects, _ any, _ RunConfig) ([]TestObjects, error) {
	return []TestObjects{
		{},
	}, nil
}

const importerPluginKey = "importer.testctl.capi"

// importerPlugin is a plugin that can be used to import another test config file.
type importerPlugin struct{}

var _ ConfigurablePlugin = &importerPlugin{}

// importerPluginPluginConfig defines the config for the importerPlugin.
type importerPluginPluginConfig struct {
	// path is the config to be imported.
	Path string `json:"path,omitempty"`
}

// ParseConfig parse the config for this plugin.
// NOTE: For the importerPlugin, this method returns the imported config file.
func (p importerPlugin) ParseConfig(_ context.Context, rawPluginConfig []byte) (any, error) {
	config := &importerPluginPluginConfig{}
	if err := yaml.UnmarshalStrict(rawPluginConfig, config); err != nil {
		return nil, err
	}

	if config.Path == "" {
		return nil, errors.New("path must be set when calling the importer plugin")
	}

	path, err := filepath.Abs(config.Path)
	if err != nil {
		return nil, errors.Errorf("failed to convert %s to an absolute path", config.Path)
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, errors.Errorf("file %s does not exist", config.Path)
	}

	importedConfigData, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read file %s", config.Path)
	}

	var importedConfig map[string]any
	if err := yaml.Unmarshal(importedConfigData, &importedConfig); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal config from %s", config.Path)
	}

	return importedConfig, nil
}

// Merge the current imported config with the current config.
// Note: if the config for the importer has a runConfig, it overrides the one from from the imported config.
func (p importerPlugin) MergeConfig(_ context.Context, config map[string]any, importedConfigRaw any) (map[string]any, error) {
	if _, ok := config["run"]; ok {
		return nil, errors.Errorf("configuration for the importer.testctl.cluster.capi plugin cannot have a run field")
	}

	importedConfig := importedConfigRaw.(map[string]any)
	if runConfig, ok := config["runConfig"]; ok {
		importedConfig["runConfig"] = runConfig
	}

	return importedConfig, nil
}

var _ SelectorPlugin = &importerPlugin{}

// Select returns a list of Objects with the same element received in input.
// NOTE: The importerPlugin implements this method only to ensure that imported config is run for the current test objects
func (r *importerPlugin) Select(_ context.Context, _ client.Client, t TestObjects, _ any, _ RunConfig) ([]TestObjects, error) {
	return []TestObjects{
		t,
	}, nil
}
