# Improving status in CAPI resources

# Summary

This documents defines how status in CAPI resources is going to evolve in the next API versions, improving usability
and consistency across different resources in CAPI and with the rest of the ecosystem.

# Motivation

Also the Cluster API community should recognize that nowadays Cluster API and Kubernetes users are rightfully focused on
building higher systems and great applications on top on those platforms, which is great.

However, as a consequence of this shifted focus, most of the users don’t have time to become deep expert of Cluster API
like the first wave of adopters, and also Cluster API maintainers would like they don’t have to.

The effect of this trend is a blurring of the lines not only between different Cluster API components, but also between
Cluster API, core Kubernetes and a few other broadly adopted tools like Helm or Flux (and in some measure, with many
other awesome tools in the ecosystem).

This is why Cluster API status must become simpler to understand for users, and also consistent not only across
different CAPI resources, but with Kubernetes core and the entire ecosystem.

Last but not least, this proposal also considers that more and more users are building monitoring/alerting on
top of Cluster API. Accordingly, the proposed changes also aims to provide signals that can be used to reason
about lifecycle events of Cluster API resources.

### Goals

- Review and standardize the usage of the concept of readiness across Cluster API resources.
    - Drop or amend improper usage of readiness
    - Make the concept of Machine readiness extensible, thus allowing providers or external system to inject their readiness checks.
- Review and standardize the usage of the concept of availability across Cluster API resources.
    - Make the concept of Cluster Availability extensible, thus allowing providers or external system to inject their availability checks.
- Bubble up more information about both CP and worker Machines, ensuring consistent way across Cluster API resources.
    - Standardize replica counters and bubble them up to the Cluster resource.
    - Standardize control plane, MachineDeployment, Machine pool availability, and bubble them up to the Cluster resource.
- Introduce missing signals about connectivity to workload clusters, thus enabling to mark all the depending conditions
  with status Unknown after a certain amount of time.
- Introduce a cleaner signal about Cluster API resources lifecycle transitions, e.g. scaling up or updating.

### Non-Goals/Future Work

- Resolving all the idiosyncrasies that exists in Cluster API, core Kubernetes, the rest of the ecosystem.
  (Let’s stay focused in Cluster API and improve incrementally).

## Proposal

This proposal groups a set of changes to status fields in Cluster API resources.

Some of those changes could be considered straight forward, e.g.

- K8s API conventions suggest to deprecate and remove `phase` fields from status, Cluster API is going to align to this recommendation
  (and improve Conditions to provide similar or even a better info as a replacement).
- K8s resources dosn’t have a concept similar to "terminal feature" existing in Cluster API resources, and users approaching
  the project are struggling with this idea; in some cases also provider's implementers are struggling with it.
  Accordingly, Cluster API resources are dropping `FailureReason` and `FailureMessage` fields (terminal failures should be surfaced using
  conditions, like any other error/warning/message)

Some other changes requires a little bit more context, which is provided in annex:

- Review and standardize the usage of the concept of readiness and availabilty. 
- Transition to K8s API conventions aligned conditions.

The last set of changes is a consequence of the above changes or as a "periodic" iteration on status fields in
Cluster API resources to improve consistency and address feedback received over time, e.g.

- Change the semantic of ReadyReplica counters to use Machine's Ready condition instead of Node's Ready condition.
  (so everywhere Ready is used for a Machine it always means the same thing)
- Add missing condition about status of the connectivity to workload clusters.
- Bubble up more information about both CP and worker Machines to the Cluster level.

In order to keep making progress on this proposal, the fist iteration will be focused on

- Machines
- MachineSets
- MachineDeployments
- MachinePools
- KubeadmControlPlane (ControlPlanes)
- Clusters

Other resources will be added as soon as there will be agreement on the general direction.

Overall, the union of all those changes, is expected to greatly improve status fields, conditions, replica counters, print columns
and more, and thus provide benefit to users interacting with the systems, monitoring tools, companies building on top of
Cluster API.

### Readiness and Availabilty

The first [condition CAEP](https://github.com/kubernetes-sigs/cluster-api/blob/main/docs/proposals/20200506-conditions.md) in Cluster API
introduced very strict requirements about “Ready” condition, mandating it exists on all resources and that it was the summary of
all the to other existing conditions.

However, over time Cluster API maintainers recognized several limitations of the “one fit all”, strict approach.

e.g. when you look at higher level abstractions in Cluster API like Clusters, MachineDeployments, ControlPlanes etc, readiness
might be confusing, because those resources usually accept a certain degree of not readiness, e.g. MachineDeployments are
usually ok even if a few machine is not ready (up to MaxUnaivailable).

Similarly, higher level abstractions in Cluster API are designed to remain operational during lifecycle operations,
e.g. when a Machine deployment is operational even if is rolling out.

Both of the use cases above where hard to combine with the strict requirement to have all the conditions true, and
as a result today Cluster APi resources barely have conditions surfacing that lifecycle operations are happening, or where
those condition are defined they have a semantic which is not easy to understand, like e.g. 'Resized' or 'MachinesSpecUpToDate'.

In order to address thi problem, this proposal is taking a different approach: the “Ready” condition won't
be required anymore to exists on all the resources, nor when it exists, it will be required to include all the existing
conditions in the ready summary.

What is key is that Condition should make sense for Cluster API users.

Accordingly, this proposal introduces an new semantic for the “Ready” condition at machine level to better represent
the "machine can host workloads" (prior art Kubernetes nodes are ready when "node can host pods"). On top of that:

- This proposal is ensuring that whenever Machine ready is used, it always means the same thing (e.g replica counters)
- In order further reduce confusion aroud usage of ready, this proposal is also changing contract fields where ready was
  used improperly to represent initial provisioning (k8s API conventions suggest to use ready only for long running process)

All in all, Machine's Ready should be much more clear, consistent, intuitive after proposed changes.
But there is more.

This proposal is also dropping Ready condition from higher level abstractions in Cluster API, while introducing a
new Available condition that better represents the fact that those objects are operational
even if there is a certain degree of not unaivailability in the system or if lifecycle operations are happening
(prior art Available condition in K8s Deployments).

Last but not least:
- With the changes to Ready and Available conditions, it is now possible to add conditions about
  surfacing that lifecycle operations are happening, e.g. scaling up.
- As suggested by K8s API conventions, this proposal is also making sure all condition are conditions are consistent and have
  uniform meaning across all resource types (and this proposal is also doing the same for replica counters and other
  status fields).

### Transition to K8s API conventions aligned conditions

K8s is undergoing an effort of standardizing usage of conditions across all resource types, and the transition to
the next API version is a great opportunity for Cluster API to align to this effort.

The value of this transition is substantial, because the differences that exists today's are really confusing for users;
those differences are also are making it harder for ecosystem tools to build on top of Cluster API, and in some cases
even confusing new (and old) contributors.

With this proposal Cluster API will close the gap with K8s api conventions with regards to:
- Polarity: Condition type names should make sense for humans; neither positive nor negative polarity can be recommended
  as a general rule (already implemented by [#10550](https://github.com/kubernetes-sigs/cluster-api/pull/10550))
- Use of the Reason field is required (currently in Cluster API reasons is added only when condition are false)
- Controllers should apply their conditions to a resource the first time they visit the resource, even if the status is Unknown.
  (currently Cluster API controllers add conditions at different stages of the reconcile loops)
- Cluster API is also dropping its own Condition type and start using metav1.Conditions from the Kubernetes API.

The last point have also another implication, which is the removal of the Severity field which is currently used
to determine priority when merging conditions.

TODO Document how we are going to replace severity (currently prototyping)

### Changes to Machine resource

#### Machine Status

Following changes are implemented to Machine's status:

- Disambiguate usage of ready term by renaming fields used for the provisioning workflow
- Align to K8s API conventions by deprecating `Phase` and corresponding `LastUpdated`
- Remove `FailureReason` and `FailureMessage` to get rid of the confusing concept of terminal failures
- Transition to new, improved, K8s API conventions aligned conditions

Below you can find the relevant fields in Machine Status v1beta2, after v1beta1 removal (end state);
After golang types, you can find a summary table that also shows how changes will be rolled out according to K8s deprecation rules.

```golang
type MachineStatus struct {

    // Initialization provides observations of the Machine initialization process.
    // NOTE: fields in this struct are part of the Cluster API contract and are used to orchestrate initial Machine provisioning.
    // The value of those fields is never updated after provisioning is completed.
    // Use conditions to monitor the operational state of the Machine's BootstrapSecret.
    // +optional
    Initialization *MachineInitializationStatus `json:"initialization,omitempty"`
    
    // Represents the observations of a Machine's current state.
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    
    // Other fields...
    // NOTE: `Phase`, `LastUpdated`, `FailureReason`, `FailureMessage` fields won't be there anymore
}

// MachineInitializationStatus provides observations of the Machine initialization process.
type MachineInitializationStatus struct {

    // BootstrapSecretCreated is true when the bootstrap provider reports that the Machine's boostrap secret is created.
    // NOTE: this field is part of the Cluster API contract and it is used to orchestrate initial Machine provisioning.
    // The value of this field is never updated after provisioning is completed.
    // Use conditions to monitor the operational state of the Machine's BootstrapSecret.
    // +optional
    BootstrapSecretCreated bool `json:"bootstrapSecretCreated"`
    
    // InfrastructureProvisioned is true when the infrastructure provider reports that the Machine's infrastructure is fully provisioned.
    // NOTE: this field is part of the Cluster API contract and it is used to orchestrate initial Machine  provisioning.
    // The value of this field is never updated after provisioning is completed.
    // Use conditions to monitor the operational state of the Machine's infrastructure.
    // +optional
    InfrastructureProvisioned bool `json:"infrastructureProvisioned"`
}
```

| v1beta1 (current)              | v1beta2 (tentative Q1 2025)                              | v1beta1 removal (tentative Q1 2026)        |
|--------------------------------|----------------------------------------------------------|--------------------------------------------|
|                                | `Initialization` (new)                                   | `Initialization`                           |
| `BootstrapReady`               | `Initialization.BootstrapSecretCreated` (renamed)        | `Initialization.BootstrapSecretCreated`    |
| `InfrastructureReady`          | `Initialization.InfrastructureProvisioned` (renamed)     | `Initialization.InfrastructureProvisioned` |
|                                | `BackCompatibilty` (new)                                 | (removed)                                  |
| `Phase` (deprecated)           | `BackCompatibilty.Phase` (renamed) (deprecated)          | (removed)                                  |
| `LastUpdated` (deprecated)     | `BackCompatibilty.LastUpdated` (renamed) (deprecated)    | (removed)                                  |
| `FailureReason` (deprecated)   | `BackCompatibilty.FailureReason` (renamed) (deprecated)  | (removed)                                  |
| `FailureMessage` (deprecated)  | `BackCompatibilty.FailureMessage` (renamed) (deprecated) | (removed)                                  |
| `Conditions`                   | `BackCompatibilty.Conditions` (renamed) (deprecated)     | (removed)                                  |
| `ExperimentalConditions` (new) | `Conditions` (renamed)                                   | `Conditions`                               |
| other fields...                | other fields...                                          | other fields...                            |

Notes:
- The `BackCompatibilty` struct is going to exist in v1beta2 types only until v1beta1 removal (9 months or 3 minor releases after v1beta2 is released/v1beta is deprecated, whichever is longer).
  Fields in this struct are used for supporting down conversions, thus providing users relying on v1beta1 APIs additional buffer time to pick up the new changes. 

##### Machine (New)Conditions

| Condition              | Note                                                                                                                                                                                                                                                                        |
|------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `Available`            | True if at the machine is Ready for at least MinReady seconds, as defined by the Machine's owner resource                                                                                                                                                                   |
| `Ready`                | True if Machine's `BootstrapSecretReady`, `InfrastructureReady`, `NodeHealthy` and `HealthCheckSucceeded` (if present) are true; if other conditions are defined in `spec.readinessGates` are defined, those conditions should be true as well for the Machine to be ready. |
| `UpToDate`             | True if the Machine spec matches the spec of the Machine's owner resource, e.g KubeadmControlPlane or MachineDeployment                                                                                                                                                     |
| `BootstrapConfigReady` | Mirrors the corresponding condition from the Machine's BootstrapConfig resource                                                                                                                                                                                             |
| `InfrastructureReady`  | Mirrors the corresponding condition from the Machine's Infrastructure resource                                                                                                                                                                                              |
| `NodeReady`            | True if the Machine's Node is ready                                                                                                                                                                                                                                         |
| `NodeHealthy`          | True if the Machine's Node is ready and it does not report MemoryPressure, DiskPressure and PIDPressure                                                                                                                                                                     |
| `HealthCheckSucceeded` | True if MHC instances targeting this machine reports the Machine is healthy according to the definition of healthy present in MHC's spec                                                                                                                                    |
| `OwnerRemediated`      |                                                                                                                                                                                                                                                                             |
| `Deleted`              | True if Machine is deleted; Reason can be used to observe the cleanup progress when the resource is deleted                                                                                                                                                                 |
| `Paused`               | True if the Machine or the Cluster it belongs to are paused                                                                                                                                                                                                                 |

> To better evaluate proposed changes, below you can find the list of current Machine's conditions:
> Ready, InfrastructureReady, NodeHealthy, PreDrainDeleteHookSucceeded, VolumeDetachSucceeded, DrainingSucceeded.
> Additionally:
> - The MachineHealthCheck controller adds the HealthCheckSucceeded and the OwnerRemediated conditions.
> - The KubeadmControlPlane adds the ApiServerPodHealthy, ControllerManagerPodHealthy, SchedulerPodHealthy, EtcdPodHealthy, EtcdMemberHealthy conditions.

Notes:
- This proposal introduces a mechanism for extending the meaning of Machine Readiness, `ReadinessGates` (see [changes to Machine.Spec](#machine-spec)).
- While `Ready` is the main signal for machines operational state, higher level abstractions in Cluster API like eg. 
  MachineDeployment are relying on the concept of Machine's `Availability`, which can be seen as readiness + stability.
  In order to standardize this concept across different higher level abstractions, this proposal is surfacing `Availability`
  condition at Machine level as well as adding a new `MinReadySeconds` field (see [changes to Machine.Spec](#machine-spec))
  that will be used to compute this condition.
- Similarly, this proposal is standardizing the concept of Machines's `UpToDate`, however in this case it will be up to
  the Machine's owner controllers to set this condition,
- Conditions like `NodeReady` and `NodeHealthy` which depends on the connection to the reomote cluster will take befefit
  of the new `ControlPlaneProbe` condition at cluster level (see [Cluster (New)Conditions](#cluster-newconditions));
  more specifically those condition should be set to Unknown after the cluster Probe fails (or after whatever period is defined in the `--remote-conditions-grace-period` flag)
- `HealthCheckSucceeded` and `OwnerRemediated` (or `ExternalRemediationRequestAvailable`) are set by the MachineHealthCheck controller in case a resource instance targets the machine.
- Also KubeadmControlPlane adds additional conditions to Machines, but those conditions are not included in the table above
  for sake of simplicity (however they are documented in the KubeadmControlPlane paragraph).

TODO: think carefully at remote conditions becoming unknown, this could block a few operations ... 

#### Machine Spec

Machine's spec is going to be improved to allow 3rd party to extend the semantic of the new Machine's `Ready` condition
as well to stardardize the concept of Machine's `Availability`.

Below you can find the relevant fields in Machine Status v1beta2, after v1beta1 removal (end state);
After golang types, you can find a summary table that also shows how changes will be rolled out according to K8s deprecation rules.

```go
type MachineSpec struct {
    
    // MinReadySeconds is the minimum number of seconds for which a Node for a newly created machine should be ready before considering the replica available.
    // Defaults to 0 (machine will be considered available as soon as the Node is ready)
    // +optional
    MinReadySeconds int32 `json:"minReadySeconds,omitempty"`

    // If specified, all readiness gates will be evaluated for Machine readiness.
    // A Machine is ready when `InfrastructureReady`, `NodeHealthy` and `HealthCheckSucceeded` (if present) are "True"; 
    // if other conditions are defined this field, those conditions should be "True" as well for the Machine to be ready.
    // +optional
    // +listType=map
    // +listMapKey=conditionType
    ReadinessGates []MachineReadinessGate `json:"readinessGates,omitempty"`

    // Other fields...
}

// MachineReadinessGate contains the reference to a Machine condition to be used as readiness gates.
type MachineReadinessGate struct {
    // ConditionType refers to a condition in the Machine's condition list with matching type.
    // Note: Both  Cluster API conditions or conditions added by 3rd party controller can be used as readiness gates.
    ConditionType string `json:"conditionType"`
}
```

| v1beta1 (current)      | v1Beta2 (tentative Q1 2025) | v1beta1 removal (tentative Q1 2026) |
|------------------------|-----------------------------|-------------------------------------|
| `ReadinessGates` (new) | `ReadinessGates`            | `ReadinessGates`                    |
| other fields...        | other fields...             | other fields...                     |

Notes:
- Both `MinReadySeconds` and `ReadinessGates` should be treated as other in-place propagated fields (changing this should not trigger rollouts).
- Similarly to Pod's `ReadinessGates`, also Machine's `ReadinessGates` accept only conditions with positive polarity; 
  The Cluster API project might revisit this in future to stay aligned with Kubernetes or if there are use cases justifying this change.

#### Machine Print columns

| Current           | To be                         |
|-------------------|-------------------------------|
| `NAME`            | `NAME`                        |
| `CLUSTER`         | `CLUSTER`                     |
| `NODE NAME`       | `PAUSED` (new) (*)            |
| `PROVIDER ID`     | `NODE NAME`                   |
| `PHASE` (deleted) | `PROVIDER ID`                 |
| `AGE`             | `READY` (new)                 |
| `VERSION`         | `AVAILABLE` (new)             |
|                   | `UP TO DATE` (new)            |
|                   | `AGE`                         |
|                   | `OS-IMAGE` (new) (*)          |
|                   | `KERNEL-VERSION` (new) (*)    |
|                   | `CONTAINER-RUNTIME` (new) (*) |

TODO: figure out if can `INTERNAL-IP` (new) (*),   `EXTERNAL-IP` after `VERSION` / before `OS-IMAGE`?  (similar to Nodes...).
might be something like `$.status.addresses[?(@.type == 'InternalIP')].address` works, but not sure what happens if there are 0 or more addresses...
  Stefan +1 if possible

(*) visible only when using `kubectl get -o wide`

### Changes to MachineSet resource

#### MachineSet Status

Following changes are implemented to MachineSet's status:

- Update `ReadyReplicas` counter to use the same semantic Machine's `Ready` (today it is computed a Machines with Node Ready) condition and add missing `UpToDateReplicas`.
- Remove `FailureReason` and `FailureMessage` to get rid of the confusing concept of terminal failures
- Transition to new, improved, K8s API conventions aligned conditions

Below you can find the relevant fields in Machine Status v1beta2, after v1beta1 removal (end state);
After golang types, you can find a summary table that also shows how changes will be rolled out according to K8s deprecation rules.

```golang
type MachineSetStatus struct {

    // The number of ready replicas for this MachineSet. A machine is considered ready when Machine's Ready condition is true.
    // +optional
    ReadyReplicas int32 `json:"readyReplicas"`

    // The number of up-to-date replicas for this MachineSet. A machine is considered up-to-date when Machine's UpToDate condition is true.
    // +optional
    UpToDateReplicas int32 `json:"upToDateReplicas"`

    // Represents the observations of a MachineSet's current state.
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // Other fields...
    // NOTE: `FailureReason`, `FailureMessage` fields won't be there anymore
}
```

| v1beta1 (current)                 | v1beta2 (tentative Q1 2025)                              | v1beta1 removal (tentative Q1 2026) |
|-----------------------------------|----------------------------------------------------------|-------------------------------------|
|                                   | `BackCompatibilty` (new)                                 | (removed)                           |
| `ReadyReplicas` (deprecated)      | `BackCompatibilty.ReadyReplicas` (renamed) (deprecated)  | (removed)                           |
| `ExperimentalReadyReplicas` (new) | `ReadyReplicas` (renamed)                                | `ReadyReplicas`                     |
| `FailureReason` (deprecated)      | `BackCompatibilty.FailureReason` (renamed) (deprecated)  | (removed)                           |
| `FailureMessage` (deprecated)     | `BackCompatibilty.FailureMessage` (renamed) (deprecated) | (removed)                           |
| `Conditions`                      | `BackCompatibilty.Conditions` (renamed) (deprecated)     | (removed)                           |
| `ExperimentalConditions` (new)    | `Conditions` (renamed)                                   | `Conditions`                        |
| `UpToDateReplicas` (new)          | `UpToDateReplicas`                                       | `UpToDateReplicas`                  |
| other fields...                   | other fields...                                          | other fields...                     |

Notes:
- The `BackCompatibilty` struct is going to exist in v1beta2 types only until v1beta1 removal (9 months or 3 minor releases after v1beta2 is released/v1beta is deprecated, whichever is longer).
  Fields in this struct are used for supporting down conversions, thus providing users relying on v1beta1 APIs additional buffer time to pick up the new changes.
- This proposal is using `UpToDateReplicas` instead of `UpdatedReplicas`; This is a deliberated choice to avoid 
  confusion between update (any change) and upgrade (change of the Kubernetes versions).
- Also `AvailableReplicas` will determine Machine's availability by reading Machine.Available condition instead of
  computing availability as of today, however in this case the semantic of the field is not changed

TODO: check `FullyLabeledReplicas`, do we still need it?

#### MachineSet (New)Conditions

| Condition        | Note                                                                                                           |
|------------------|----------------------------------------------------------------------------------------------------------------|
| `ReplicaFailure` | This condition surfaces issues on creating Machines, if any.                                                   |
| `MachinesReady`  | This condition surfaces detail of issues on the controlled machines, if any.                                   |
| `ScalingUp`      | True if available replicas < desired replicas                                                                  |
| `ScalingDown`    | True if replicas > desired replicas                                                                            |
| `UpToDate`       | True if all the Machines controlled by this MachineSet are up to date (replicas = upToDate replicas)           |
| `Remediating`    | True if there is at least one Machine controlled by this MachineSet is not passing health checks               |
| `Deleted`        | True if MachineSet is deleted; Reason can be used to observe the cleanup progress when the resource is deleted |
| `Paused`         | True if this MachineSet or the Cluster it belongs to are paused                                                |

> To better evaluate proposed changes, below you can find the list of current MachineSet's conditions:
> Ready, MachinesCreated, Resized, MachinesReady.

Notes:
- MachineSet conditions are intentionally mostly consistent with MachineDeployment conditions to help users troubleshooting .
- MachineSet is considered as a sort of implementation detail of MachineDeployments, so it doesn't have is own concept of availability.
  Similarly, this proposal is dropping the notion of MachineSet readiness because it is preferred to let users to focus on Machines readiness.
- `Remediating` for older MachineSet sets will report that remediation will happen as part of the regular rollout.
- `UpToDate` condition initially will be `false` for older MachineSet, `true` for the current MachineSet; however in
  the future the latter might evolve in case Cluster API will start supporting in-place upgrades.

#### MachineSet Print columns

| Current       | To be                   |
|---------------|-------------------------|
| `NAME`        | `NAME`                  |
| `CLUSTER`     | `CLUSTER`               |
| `DESIRED` (*) | `PAUSED` (new) (*)      |
| `REPLICAS`    | `DESIRED`               |
| `READY`       | `CURRENT` (renamed) (*) |
| `AVAILABLE`   | `READY` (updated)       |
| `AGE`         | `AVAILABLE` (updated)   |
| `VERSION`     | `UP-TO-DATE` (new)      |
|               | `AGE`                   |
|               | `VERSION`               |

(*) visible only when using `kubectl get -o wide`

Notes:
- In k8s Deployment and ReplicaSet have different print columns for replica counters; this proposal enforces replicas
  counter columns consistent across all resources.

### Changes to MachineDeployment resource

#### MachineDeployment Status

Following changes are implemented to MachineDeployment's status:

- Align `UpdatedReplicas` to use Machine's `UpToDate` condition (and rename it accordingly)
- Align to K8s API conventions by deprecating `Phase`
- Remove `FailureReason` and `FailureMessage` to get rid of the confusing concept of terminal failures
- Transition to new, improved, K8s API conventions aligned conditions

Below you can find the relevant fields in Machine Status v1beta2, after v1beta1 removal (end state);
After golang types, you can find a summary table that also shows how changes will be rolled out according to K8s deprecation rules.

```golang
type MachineDeploymentStatus struct {

    // The number of up-to-date replicas targeted by this deployment.
    // +optional
    UpToDateReplicas int32 `json:"upToDateReplicas"`

    // Represents the observations of a MachineDeployment's current state.
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // Other fields...
    // NOTE: `Phase`, `FailureReason`, `FailureMessage` fields won't be there anymore
}
```

| v1beta1 (current)              | v1beta2 (tentative Q1 2025)                              | v1beta1 removal (tentative Q1 2026) |
|--------------------------------|----------------------------------------------------------|-------------------------------------|
| `UpdatedReplicas`              | `UpToDateReplicas` (renamed)                             | `UpToDateReplicas`                  |
| `Phase` (deprecated)           | `BackCompatibilty.Phase` (renamed) (deprecated)          | (removed)                           |
| `FailureReason` (deprecated)   | `BackCompatibilty.FailureReason` (renamed) (deprecated)  | (removed)                           |
| `FailureMessage` (deprecated)  | `BackCompatibilty.FailureMessage` (renamed) (deprecated) | (removed)                           |
| `Conditions`                   | `BackCompatibilty.Conditions` (renamed) (deprecated)     | (removed)                           |
| `ExperimentalConditions` (new) | `Conditions` (renamed)                                   | `Conditions`                        |
| other fields...                | other fields...                                          | other fields...                     |

Notes:
- The `BackCompatibilty` struct is going to exist in v1beta2 types only until v1beta1 removal (9 months or 3 minor releases after v1beta2 is released/v1beta is deprecated, whichever is longer).
  Fields in this struct are used for supporting down conversions, thus providing users relying on v1beta1 APIs additional buffer time to pick up the new changes.

#### MachineDeployment (New)Conditions

| Condition        | Note                                                                                                                                                                                                                                                    |
|------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `Available`      | True if the MachineDeployment has minimum availability according to parameters specified in the deployment strategy, e.g. If using RollingUpagrade strategy, availableReplicas must be greater or equal than desired replicas - MaxUnavailable replicas |  
| `ReplicaFailure` | This condition surfaces issues on creating Machines controlled by this MachineDeployment, if any.                                                                                                                                                       |
| `MachinesReady`  | This condition surfaces detail of issues on the controlled machines, if any.                                                                                                                                                                            |
| `ScalingUp`      | True if available replicas < desired replicas                                                                                                                                                                                                           |
| `ScalingDown`    | True if replicas > desired replicas                                                                                                                                                                                                                     |
| `UpToDate`       | True if all the Machines controlled by this MachineDeployment are up to date (replicas = upToDate replicas)                                                                                                                                             |
| `Remediating`    | True if there is at least one machine controlled by this MachineDeployment is not passing health checks                                                                                                                                                 |
| `Deleted`        | True if MachineDeployment is deleted; Reason can be used to observe the cleanup progress when the resource is deleted                                                                                                                                   |
| `Paused`         | True if this MachineDeployment or the Cluster it belongs to are paused                                                                                                                                                                                  |

> To better evaluate proposed changes, below you can find the list of current MachineDeployment's conditions:
> Ready, Available.

#### MachineDeployment Print columns

| Current                 | To be                  |
|-------------------------|------------------------|
| `NAME`                  | `NAME`                 |
| `CLUSTER`               | `CLUSTER`              |
| `DESIRED` (*)           | `PAUSED` (new) (*)     |
| `REPLICAS`              | `DESIRED`              |
| `READY`                 | `CURRENT` (*)          |
| `UPDATED` (renamed)     | `READY`                |
| `UNAVAILABLE` (deleted) | `AVAILABLE` (new)      |
| `PHASE` (deleted)       | `UP-TO-DATE` (renamed) |
| `AGE`                   | `AGE`                  |
| `VERSION`               | `VERSION`              |

TODO: consider if to add Machine deployment `AVAILABLE`, but we should find a way to differentiate from `AVAILABLE` replicas
  Stefan +1 to have AVAILABLE, not sure if we can have two columns with the same header

(*) visible only when using `kubectl get -o wide`

### Changes to Cluster resource

#### Cluster Status

Following changes are implemented to Cluster's status:

- Disambiguate usage of ready term by renaming fields used for the provisioning workflow
- Align to K8s API conventions by deprecating `Phase` and corresponding `LastUpdated`
- Remove `FailureReason` and `FailureMessage` to get rid of the confusing concept of terminal failures
- Transition to new, improved, K8s API conventions aligned conditions
- Add replica counters to surface status of Machines belonging to this Cluster
- Surface information about ControlPlane connection heartbeat (see new conditions)

Below you can find the relevant fields in Machine Status v1beta2, after v1beta1 removal (end state);
After golang types, you can find a summary table that also shows how changes will be rolled out according to K8s deprecation rules.

```golang
type ClusterStatus struct {

    // Initialization provides observations of the Cluster initialization process.
    // NOTE: fields in this struct are part of the Cluster API contract and are used to orchestrate initial Cluster provisioning.
    // The value of those fields is never updated after provisioning is completed.
    // Use conditions to monitor the operational state of the Cluster's BootstrapSecret.
    // +optional
    Initialization *MachineInitializationStatus `json:"initialization,omitempty"`
    
    // Represents the observations of a Cluster's current state.
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    
    // ControlPlane groups all the observations about Cluster's ControlPlane current state.
    // +optional
    ControlPlane ClusterControlPlaneStatus `json:"controlPlane,omitempty"`
    
    // Workers groups all the observations about Cluster's Workers current state.
    // +optional
    Workers ClusterControlPlaneStatus `json:"workers,omitempty"`
    
    // other fields
}

// ClusterInitializationStatus provides observations of the Cluster initialization process.
type ClusterInitializationStatus struct {

    // InfrastructureProvisioned is true when the infrastructure provider reports that Cluster's infrastructure is fully provisioned.
    // NOTE: this field is part of the Cluster API contract and it is used to orchestrate provisioning.
    // The value of this field is never updated after provisioning is completed.
    // +optional
    InfrastructureProvisioned bool `json:"infrastructureProvisioned"`
    
    // ControlPlaneInitialized denotes when the control plane is functional enough to accept requests.
    // This information is usually used as a signal for starting all the provisioning operations that depends on
    // a functional API server, but do not require a full HA control plane to exists, like e.g. join worker Machines,
    // install core addons like CNI, CPI, CSI etc.
    // NOTE: this field is part of the Cluster API contract and it is used to orchestrate provisioning.
    // The value of this field is never updated after provisioning is completed.
    // +optional
    ControlPlaneInitialized bool `json:"controlPlaneInitialized"`
}

// ClusterControlPlaneStatus groups all the observations about control plane current state.
type ClusterControlPlaneStatus struct {
    // Total number of desired control plane machines in this cluster.
    // +optional
    DesiredReplicas int32 `json:"desiredReplicas"`

    // Total number of non-terminated control plane machines in this cluster.
    // +optional
    Replicas int32 `json:"replicas"`
    
    // The number of up-to-date control plane machines in this cluster.
    // +optional
    UpToDateReplicas int32 `json:"upToDateReplicas"`
    
    // Total number of ready control plane machines in this cluster.
    // +optional
    ReadyReplicas int32 `json:"readyReplicas"`
    
    // Total number of available control plane machines in this cluster.
    // +optional
    AvailableReplicas int32 `json:"availableReplicas"`
    
    // Total number of unavailable control plane machines in this cluster.
    // +optional
    UnavailableReplicas int32 `json:"unavailableReplicas"`
}

// WorkersPlaneStatus groups all the observations about workers current state.
type WorkersPlaneStatus struct {
    // Total number of desired worker machines in this cluster.
    // +optional
    DesiredReplicas int32 `json:"desiredReplicas"`

    // Total number of non-terminated worker machines in this cluster.
    // +optional
    Replicas int32 `json:"replicas"`
    
    // The number of up-to-date worker machines in this cluster.
    // +optional
    UpToDateReplicas int32 `json:"upToDateReplicas"`
    
    // Total number of ready worker machines in this cluster.
    // +optional
    ReadyReplicas int32 `json:"readyReplicas"`
    
    // Total number of available worker machines in this cluster.
    // +optional
    AvailableReplicas int32 `json:"availableReplicas"`
    
    // Total number of unavailable worker machines in this cluster.
    // +optional
    UnavailableReplicas int32 `json:"unavailableReplicas"`
}
```

// TODO: check about "non-terminated" for replicas fields.

| v1beta1 (current)                        | v1beta2 (tentative Q1 2025)                              | v1beta1 removal (tentative Q1 2026)        |
|------------------------------------------|----------------------------------------------------------|--------------------------------------------|
|                                          | `Initialization` (new)                                   | `Initialization`                           |
| `InfrastructureReady`                    | `Initialization.InfrastructureProvisioned` (renamed)     | `Initialization.InfrastructureProvisioned` |
| `ControlPlaneReady`                      | `Initialization.ControlPlaneInitialized` (renamed)       | `Initialization.ControlPlaneInitialized`   |
|                                          | `BackCompatibilty` (new)                                 | (removed)                                  |
| `Phase` (deprecated)                     | `BackCompatibilty.Phase` (renamed) (deprecated)          | (removed)                                  |
| `FailureReason` (deprecated)             | `BackCompatibilty.FailureReason` (renamed) (deprecated)  | (removed)                                  |
| `FailureMessage` (deprecated)            | `BackCompatibilty.FailureMessage` (renamed) (deprecated) | (removed)                                  |
| `Conditions`                             | `BackCompatibilty.Conditions` (renamed) (deprecated)     | (removed)                                  |
| `ExperimentalConditions` (new)           | `Conditions` (renamed)                                   | `Conditions`                               |
| `ControlPlane` (new)                     | `ControlPlane`                                           | `ControlPlane`                             |
| `ControlPlane.DesiredReplicas` (new)     | `ControlPlane.DesiredReplicas`                           | `ControlPlane.DesiredReplicas`             |
| `ControlPlane.Replicas` (new)            | `ControlPlane.Replicas`                                  | `ControlPlane.Replicas`                    |
| `ControlPlane.ReadyReplicas` (new)       | `ControlPlane.ReadyReplicas`                             | `ControlPlane.ReadyReplicas`               |
| `ControlPlane.UpToDateReplicas` (new)    | `ControlPlane.UpToDateReplicas`                          | `ControlPlane.UpToDateReplicas`            |
| `ControlPlane.AvailableReplicas` (new)   | `ControlPlane.AvailableReplicas`                         | `ControlPlane.AvailableReplicas`           |
| `ControlPlane.UnavailableReplicas` (new) | `ControlPlane.UnavailableReplicas`                       | `ControlPlane.UnavailableReplicas`         |
| `Workers` (new)                          | `Workers`                                                | `Workers`                                  |
| `Workers.DesiredReplicas` (new)          | `Workers.DesiredReplicas`                                | `Workers.DesiredReplicas`                  |
| `Workers.Replicas` (new)                 | `Workers.Replicas`                                       | `Workers.Replicas`                         |
| `Workers.ReadyReplicas` (new)            | `Workers.ReadyReplicas`                                  | `Workers.ReadyReplicas`                    |
| `Workers.UpToDateReplicas` (new)         | `Workers.UpToDateReplicas`                               | `Workers.UpToDateReplicas`                 |
| `Workers.AvailableReplicas` (new)        | `Workers.AvailableReplicas`                              | `Workers.AvailableReplicas`                |
| `Workers.UnavailableReplicas` (new)      | `Workers.UnavailableReplicas`                            | `Workers.UnavailableReplicas`              |
| other fields...                          | other fields...                                          | other fields...                            |

notes:
- The `BackCompatibilty` struct is going to exist in v1beta2 types only until v1beta1 removal (9 months or 3 minor releases after v1beta2 is released/v1beta is deprecated, whichever is longer).
  Fields in this struct are used for supporting down conversions, thus providing users relying on v1beta1 APIs additional buffer time to pick up the new changes.

##### Cluster (New)Conditions

| Condition                 | Note                                                                                                                                                                                                                                                                                                                    |
|---------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `Available`               | True if Cluster `ControlPlaneHealthCheck` is true, if Cluster's control plane `Available` condition is true, if all MachineDeployment and MachinePool's `Available` condition are true; if conditions are defined in `spec.availabilityGates`, those conditions should be true as well for the Cluster to be available. |
| `ControlPlaneInitialized` | True when the Cluster's control plane is functional enough to accept requests. This information is usually used as a signal for starting all the provisioning operations that depends on a functional API server, but do not require a full HA control plane to exists.                                                 |
| `ControlPlaneProbe`       | True when control plane can be reached; in case of connection problems, the condition turns to false only if the the cluster cannot be reached for 40s after the first connection problem is detected (or whatever period is defined in the `--cluster-probe-grace-period` flag) the cluster cannot be reached          |
| `ControlPlaneAvailable`   | Mirror of Cluster's control plane `Available` condition                                                                                                                                                                                                                                                                 |
| `WorkersAvaiable`         | Summary of MachineDeployment and MachinePool's `Available` condition                                                                                                                                                                                                                                                    |
| `TopologyReconciled`      |                                                                                                                                                                                                                                                                                                                         |
| `ScalingUp`               | True if available replicas < desired replicas                                                                                                                                                                                                                                                                           |
| `ScalingDown`             | True if replicas > desired replicas                                                                                                                                                                                                                                                                                     |
| `UpToDate`                | True if all the Machines controlled by this Cluster are up to date (replicas = upToDate replicas)                                                                                                                                                                                                                       |
| `Remediating`             | True if there is at least one machine controlled by this Cluster is not passing health checks                                                                                                                                                                                                                           |
| `Deleted`                 | True if Cluster is deleted; Reason can be used to observe the cleanup progress when the resource is deleted                                                                                                                                                                                                             |
| `Paused`                  | True if Cluster and all the resources being part of it are paused                                                                                                                                                                                                                                                       |

> To better evaluate proposed changes, below you can find the list of current Cluster's conditions:
> Ready, InfrastructureReady, ControlPlaneReady, ControlPlaneInitialized, TopologyReconciled

Notes:
- `TopologyReconciled` exists only for classy clusters; this condition is managed by the topology reconciler.
- Cluster API is going to maintain a `lastControlPlaneProbeTime` to avoid flakes on `ControlPlaneProbe`.
- Similarly to `lastHeartbeatTime` in Kubernetes conditions, also `lastControlPlaneProbeTime` will not surfaces on the 
  API in order to avoid costly, continuos reconcile events.

#### Cluster Spec

Cluster's spec is going to be improved to allow 3rd party to extend the semantic of the new Cluster's `Available` condition.

Below you can find the relevant fields in Machine Status v1beta2, after v1beta1 removal (end state);
After golang types, you can find a summary table that also shows how changes will be rolled out according to K8s deprecation rules.

| v1beta1 (current)         | v1Beta2 (tentative Q1 2025) | v1beta1 removal (tentative Q1 2026) |
|---------------------------|-----------------------------|-------------------------------------|
| `AvailabilityGates` (new) | `AvailabilityGates`         | `AvailabilityGates`                 |
| other fields...           | other fields...             | other fields...                     |

```golang
type ClusterSpec struct {
    // If specified, all availability gates will be evaluated for Cluster readiness.
    // A Cluster is available when True if Cluster `ControlHeartbeat` and `TopologyReconciled` are true, if Cluster's 
    // control plane `Available` condition is true, if all worker resources's `Available` condition are true;
    // if conditions are defined in `spec.availabilityGates` are defined, those conditions should be true as well.
    // +optional
    // +listType=map
    // +listMapKey=conditionType
    AvailabilityGates []ClusterAvailabilityGate `json:"availabilityGates,omitempty"`

    // Other fields...
}

// ClusterAvailabilityGate contains the reference to a Cluster condition to be used as availability gates.
type ClusterAvailabilityGate struct {
    // ConditionType refers to a condition in the Cluster's condition list with matching type.
    // Note: Both Cluster API conditions or conditions added by 3rd party controller can be used as availability gates. 
    ConditionType string `json:"conditionType"`
}
```

Notes:
- Similarly to Pod's `ReadinessGates`, also Machine's `AvailabilityGates` accept only conditions with positive polarity;
  The Cluster API project might revisit this in future to stay aligned with Kubernetes or if there are use cases justifying this change.
- In future the Cluster API project might consider ways to make `AvailabilityGates` configurable at ClusterClass level, but
  this can be implemented as a follow-up.

#### Cluster Print columns

| Current           | To be                 |
|-------------------|-----------------------|
| `NAME`            | `NAME`                |
| `CLUSTER CLASS`   | `CLUSTER CLASS`       |
| `PHASE` (deleted) | `PAUSED` (new) (*)    |
| `AGE`             | `AVAILABLE` (new)     |
| `VERSION`         | `CP_DESIRED` (new)    |
|                   | `CP_CURRENT`(new) (*) |
|                   | `CP_READY` (new) (*)  |
|                   | `CP_AVAILABLE` (new)  |
|                   | `CP_UP_TO_DATE` (new) |
|                   | `W_DESIRED` (new)     |
|                   | `W_CURRENT`(new) (*)  |
|                   | `W_READY` (new) (*)   |
|                   | `W_AVAILABLE` (new)   |
|                   | `W_UP_TO_DATE` (new)  |
|                   | `AGE`                 |
|                   | `VERSION`             |

(*) visible only when using `kubectl get -o wide`

### Changes to KubeadmControlPlane (KCP) resource

#### KubeadmControlPlane Status

Following changes are implemented to MachineSet's status:

- TODO: figure out what to do with contract fields + conditions
- Update `ReadyReplicas` counter to use the same semantic Machine's `Ready` condition and add missing `UpToDateReplicas`.
- Remove `FailureReason` and `FailureMessage` to get rid of the confusing concept of terminal failures
- Transition to new, improved, K8s API conventions aligned conditions

Below you can find the relevant fields in Machine Status v1beta2, after v1beta1 removal (end state);
After golang types, you can find a summary table that also shows how changes will be rolled out according to K8s deprecation rules.

```golang
type KubeadmControlPlaneStatus struct {

    // The number of ready replicas for this ControlPlane. A machine is considered ready when Machine's Ready condition is true.
    // Note: In the v1beta1 API version a Machine was counted as ready when the node hosted on the Machine was ready, thus 
    // generating confusion for users looking at the Machine.Ready condition.
    // +optional
    ReadyReplicas int32 `json:"readyReplicas"`

    // The number of available replicas targeted by this ControlPlane.
    // +optional
    AvailableReplicas int32 `json:"availableReplicas"`
	
    // The number of up-to-date replicas targeted by this ControlPlane.
    // +optional
    UpToDateReplicas int32 `json:"upToDateReplicas"`

    // Represents the observations of a ControlPlane's current state.
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // Other fields...
	// NOTE: `Ready`, `FailureReason`, `FailureMessage` fields won't be there anymore
}
```

| v1beta1 (current)                 | v1beta2 (tentative Q1 2025)                              | v1beta1 removal (tentative Q1 2026) |
|-----------------------------------|----------------------------------------------------------|-------------------------------------|
| `Ready` (deprecated)              | `Ready` (deprecated)                                     | (removed)                           |
|                                   | `BackCompatibilty` (new)                                 | (removed)                           |
| `ReadyReplicas` (deprecated)      | `BackCompatibilty.ReadyReplicas` (renamed) (deprecated)  | (removed)                           |
| `ExperimentalReadyReplicas` (new) | `ReadyReplicas` (renamed)                                | `ReadyReplicas`                     |
| `FailureReason` (deprecated)      | `BackCompatibilty.FailureReason` (renamed) (deprecated)  | (removed)                           |
| `FailureMessage` (deprecated)     | `BackCompatibilty.FailureMessage` (renamed) (deprecated) | (removed)                           |
| `Conditions`                      | `BackCompatibilty.Conditions` (renamed) (deprecated)     | (removed)                           |
| `UpdatedReplicas`                 | `UpToDateReplicas` (renamed)                             | `UpToDateReplicas`                  |
| `AvailableReplicas` (new)         | `AvailableReplicas`                                      | `AvailableReplicas`                 |
| other fields...                   | other fields...                                          | other fields...                     |

TODO: double check usages of status.ready.

#### KubeadmControlPlane (New)Conditions

| Condition                       | Note                                                                                                                    |
|---------------------------------|-------------------------------------------------------------------------------------------------------------------------|
| `Available`                     | True if the control plane can be reached and there is etcd quorum, and `CertificatesAvailable` is true                  |
| `CertificatesAvailable`         | True if all the cluster certificates exist.                                                                             |
| `ReplicaFailure`                | This condition surfaces issues on creating Machines controlled by this KubeadmControlPlane, if any.                     |
| `Initialized`                   | True ControlPlaneComponentsHealthy.                                                                                     |
| `ControlPlaneComponentsHealthy` | This condition surfaces detail of issues on the controlled machines, if any.                                            |
| `EtcdClusterHealthy`            | This condition surfaces detail of issues on the controlled machines, if any.                                            |
| `MachinesReady`                 | This condition surfaces detail of issues on the controlled machines, if any.                                            |
| `ScalingUp`                     | True if available replicas < desired replicas                                                                           |
| `ScalingDown`                   | True if replicas > desired replicas                                                                                     |
| `UpToDate`                      | True if all the Machines controlled by this ControlPlane are up to date                                                 |
| `Remediating`                   | True if there is at least one machine controlled by this KubeadmControlPlane is not passing health checks               |
| `Deleted`                       | True if KubeadmControlPlane is deleted; Reason can be used to observe the cleanup progress when the resource is deleted |
| `Paused`                        | True if this resource or the Cluster it belongs to are paused                                                           |

> To better evaluate proposed changes, below you can find the list of current KubeadmControlPlane's conditions:
> Ready, CertificatesAvailable, MachinesCreated, Available, MachinesSpecUpToDate, Resized, MachinesReady,
> ControlPlaneComponentsHealthy, EtcdClusterHealthy.

Notes:
- `ControlPlaneComponentsHealthy` and `EtcdClusterHealthy` have a very strict semantic: everything should be ok for the condition to be true;
  This means it is expected those condition to flick while performing lifecycle operations; over time we might consider changes to make
  those conditions to distinguish more accurately health issues vs "expected" temporary unaivailability.

#### KubeadmControlPlane Print columns

| Current                  | To be                  |
|--------------------------|------------------------|
| `NAME`                   | `NAME`                 |
| `CLUSTER`                | `CLUSTER`              |
| `DESIRED` (*)            | `PAUSED` (new) (*)     |
| `REPLICAS`               | `INITIALIZED` (new)    |
| `READY`                  | `DESIRED`              |
| `UPDATED` (renamed)      | `CURRENT` (*)          |
| ``UNAVAILABLE` (deleted) | `READY`                |
| `PHASE` (deleted)        | `AVAILABLE` (new)      |
| `AGE`                    | `UP-TO-DATE` (renamed) |
| `VERSION`                | `AGE`                  |
|                          | `VERSION`              |

(*) visible only when using `kubectl get -o wide`

### Changes to MachinePool resource

TODO

### Changes to Cluster API contract

TODO
