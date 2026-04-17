# Idea: make it pluggable, extensible

```yaml
run:
  - plugin: 
    ...
```

Top level struct is well-know
Items partially known
- type -> link to a list of plugins, each own with its specific knowledge of config and what to do 


```yaml
---
run:
  - plugin: clusterSelector
    description: select first 50 clusters
    ...
---
run:
  - plugin: clusterCreator
    description: create 10 cluster
    ...
```

Each plugin can be a runner or an executor

Executor performs actions.
Runners returns a list of items (list of items, where each item is list of k/v pairs); for each item they run the nested `run: ` list 

There might be a `runConfig:` as well (both top level or nested)

```yaml
runConfig:
  concurrency: 1
  onError: 
    fail: false
    pause: false
  breakpoint:
    ...
run:
```