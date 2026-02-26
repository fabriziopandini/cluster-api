/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"fmt"
	"os"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/e2ectl/test"
)

var (
	logOptions = logs.NewOptions()
	scheme     = runtime.NewScheme()
	rc         = runOptions{}
)

type stackTracer interface {
	StackTrace() errors.StackTrace
}

// Execute executes the root command.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		if err, ok := err.(stackTracer); ok {
			for _, f := range err.StackTrace() {
				fmt.Fprintf(os.Stderr, "%+s:%d\n", f, f)
			}
		}
		os.Exit(1)
	}
}

// RootCmd is clusterctl root CLI command.
var RootCmd = &cobra.Command{
	Use:          "e2ectl",
	SilenceUsage: true,
	Short:        "e2ectl runs Cluster API e2e scenarios",
	RunE:         runE2E,
}

type runOptions struct {
	kubeconfig        string
	kubeconfigContext string
	config            string
	dryRun            bool
}

func init() {
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	logsv1.AddFlags(logOptions, RootCmd.Flags())

	RootCmd.Flags().StringVar(&rc.kubeconfig, "kubeconfig", "",
		"Path to a kubeconfig file to use for the management cluster. If empty, default discovery rules apply.")
	RootCmd.Flags().StringVar(&rc.kubeconfigContext, "kubeconfig-context", "",
		"Context to be used within the kubeconfig file. If empty, current context will be used.")
	RootCmd.Flags().StringVarP(&rc.config, "config", "c", "",
		"The config file with the e2e test sequence to be run.")
	RootCmd.Flags().BoolVar(&rc.dryRun, "dry-run", false, "Dry run the e2e test")
}

func runE2E(_ *cobra.Command, _ []string) error {
	if err := logsv1.ValidateAndApply(logOptions, nil); err != nil {
		return err
	}

	// TODO: Validate other flags

	ctrl.SetLogger(klog.Background())
	ctx := ctrl.SetupSignalHandler()

	configLoadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if rc.kubeconfig != "" {
		configLoadingRules.ExplicitPath = rc.kubeconfig
	}

	config, err := configLoadingRules.Load()
	if err != nil {
		return errors.Wrap(err, "failed to load Kubeconfig")
	}

	contextName := config.CurrentContext
	if rc.kubeconfigContext != "" {
		contextName = rc.kubeconfigContext
	}

	context, ok := config.Contexts[contextName]
	if !ok {
		if rc.kubeconfig != "" {
			return errors.Errorf("failed to get context %q from %q", contextName, configLoadingRules.GetExplicitFile())
		}
		return errors.Errorf("failed to get context %q from %q", contextName, configLoadingRules.GetLoadingPrecedence())
	}

	restConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{
		Context: *context,
	}).ClientConfig()
	if err != nil {
		return errors.Wrapf(err, "failed to create rest config from %q", configLoadingRules.GetExplicitFile())
	}
	restConfig.UserAgent = "e2ectl"
	restConfig.QPS = 20
	restConfig.Burst = 100

	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return errors.Wrapf(err, "failed to create client from %q", configLoadingRules.GetExplicitFile())
	}

	testConfig, err := test.ReadConfig(rc.config)
	if err != nil {
		return errors.Wrapf(err, "failed to read test config file %s", rc.config)
	}

	return test.Run(ctx, c, testConfig, rc.dryRun)
}
