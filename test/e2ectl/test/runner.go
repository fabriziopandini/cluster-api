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

package test

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// Run tests on CAPI Clusters..
func Run(ctx context.Context, c client.Client, testConfig *Config, dryRun bool) error {
	// TODO: validate options

	if len(testConfig.Tests) == 0 {
		return nil
	}

	_, err := newTaskRunner[testClustersInput, testClusterOutput](
		int32(len(testConfig.Tests)), // All tests must be processed in parallel, setting concurrency accordingly.
		false,                        // Always try to complete all tests, no matter if one fails.
		runTestCluster,
	).Run(ctx, c, newTestClustersInputs(testConfig), dryRun)

	return err
}

type testClustersInput struct {
	config ActionList
}

type testClusterOutput struct{}

func newTestClustersInputs(testConfig *Config) (r []testClustersInput) {
	for _, c := range testConfig.Tests {
		r = append(r, testClustersInput{config: c})
	}
	return
}

func runTestCluster(ctx context.Context, c client.Client, input testClustersInput, dryRun bool) (*testClusterOutput, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("test", input.config.Name)
	ctx = ctrl.LoggerInto(ctx, log)

	clusters, err := getClusters(ctx, c, input.config.ClusterNameRegex, input.config.LimitClusters)
	if err != nil {
		return nil, err
	}

	log.Info(fmt.Sprintf("Test case %s, %d candidate clusters", input.config.Name, len(clusters)))

	if _, err := newTaskRunner[runClusterActionsInput, runClusterActionsOutput](
		ptr.Deref(input.config.Concurrency, 1),
		ptr.Deref(input.config.FailFast, false),
		runClusterActions,
	).Run(ctx, c, newRunClusterActionInputs(clusters, input.config.Actions), dryRun); err != nil {
		return nil, errors.Wrapf(err, "failed to run test case %s", input.config.Name)
	}

	log.Info(fmt.Sprintf("Test case %s completed on %d clusters", input.config.Name, len(clusters)))

	return nil, nil
}

func getClusters(ctx context.Context, c client.Client, nameRegEx string, limit *int32) ([]*clusterv1.Cluster, error) {
	clusterList := &clusterv1.ClusterList{}
	if err := c.List(ctx, clusterList); err != nil {
		return nil, errors.Wrap(err, "failed to list Clusters")
	}

	var regex *regexp.Regexp
	if nameRegEx != "" {
		var err error
		regex, err = regexp.Compile(nameRegEx)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse nameRegex %s", nameRegEx)
		}
	}

	clusters := make([]*clusterv1.Cluster, 0, len(clusterList.Items))
	for _, cluster := range clusterList.Items {
		if regex != nil && !regex.MatchString(cluster.Name) {
			continue
		}

		clusters = append(clusters, &cluster)
		if limit != nil && len(clusters) >= int(ptr.Deref(limit, 0)) {
			break
		}
	}
	return clusters, nil
}

type runClusterActionsInput struct {
	cluster *clusterv1.Cluster
	actions []ClusterTestActionConfig
}

type runClusterActionsOutput struct{}

func newRunClusterActionInputs(clusters []*clusterv1.Cluster, actions []ClusterTestActionConfig) (r []runClusterActionsInput) {
	for _, cluster := range clusters {
		r = append(r, runClusterActionsInput{
			cluster: cluster,
			actions: actions,
		})
	}
	return
}

type taskProcessingFunc[T, R any] func(context.Context, client.Client, T, bool) (*R, error)

func newTaskRunner[T, R any](concurrency int32, failFast bool, f taskProcessingFunc[T, R]) taskRunner[T, R] {
	return taskRunner[T, R]{
		concurrency: concurrency,
		failFast:    failFast,
		f:           f,
	}
}

type taskRunner[T, R any] struct {
	concurrency int32
	failFast    bool
	f           taskProcessingFunc[T, R]
}

type taskResult[R any] struct {
	Error error
	Value *R
}

func (r taskRunner[T, R]) Run(ctx context.Context, c client.Client, input []T, dryRun bool) ([]*R, error) {
	// Start a channel. This channel will be used to coordinate work with the workers.
	// The channel is used to communicate the value to be processed by each task.
	inputChan := make(chan T)
	wg := &sync.WaitGroup{}
	doneChan := make(chan bool)
	resultChan := make(chan taskResult[R])

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start the workers.
	for range r.concurrency {
		wg.Add(1)
		go func(ctx context.Context, inputChan chan T, errChan chan taskResult[R], wg *sync.WaitGroup) {
			defer wg.Done()

			for {
				done := func() bool {
					select {
					case <-ctx.Done():
						// If the context is canceled, return and shutdown the worker.
						return true
					case i, open := <-inputChan:
						// If the channel is closed it implies there is no more work to be done. return and shutdown the worker..
						if !open {
							return true
						}

						value, err := r.f(ctx, c, i, dryRun)
						errChan <- taskResult[R]{
							Value: value,
							Error: err,
						}
						return false
					}
				}()
				if done {
					break
				}
			}
		}(ctx, inputChan, resultChan, wg)
	}

	// Adding the value into the input channel.
	go func() {
		for _, v := range input {
			inputChan <- v
		}
		close(inputChan)
	}()

	// Wait for processing to complete.
	go func() {
		wg.Wait()
		close(doneChan)
	}()

	results := []*R{}
	errs := []error{}

outer:
	for {
		select {
		case result := <-resultChan:
			results = append(results, result.Value)
			if result.Error != nil {
				errs = append(errs, result.Error)
				if r.failFast {
					cancel()
				}
			}
		case <-doneChan:
			break outer
		}
	}

	// Close the result channel.
	close(resultChan)

	return results, kerrors.NewAggregate(errs)
}
