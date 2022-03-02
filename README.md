# cluster-backup-operator
Cluster Back up and Restore Operator. 
------

<!-- START doctoc generated TOC please keep comment here to allow auto update -->
<!-- DON'T EDIT THIS SECTION, INSTEAD RE-RUN doctoc TO UPDATE -->
**Table of Contents**  *generated with [DocToc](https://github.com/thlorenz/doctoc)*

  - [Work in Progress](#work-in-progress)
  - [Community, discussion, contribution, and support](#community-discussion-contribution-and-support)
  - [License](#license)
  - [Getting Started](#getting-started)
  - [Design](#design)
    - [What is backed up](#what-is-backed-up)
    - [Scheduling a cluster backup](#scheduling-a-cluster-backup)
      - [Backup Collisions](#backup-collisions)
    - [Restoring a backup](#restoring-a-backup)
      - [Cleaning up the hub before restore](#cleaning-up-the-hub-before-restore)
    - [Backup validation using a Policy](#backup-validation-using-a-policy)
- [Setting up Your Dev Environment](#setting-up-your-dev-environment)
  - [Prerequiste Tools](#prerequiste-tools)
  - [Installation](#installation)
    - [Outside the Cluster](#outside-the-cluster)
    - [Inside the Cluster](#inside-the-cluster)
- [Usage](#usage)
- [Testing](#testing)

<!-- END doctoc generated TOC please keep comment here to allow auto update -->

------

## Work in Progress
We are in the process of enabling this repo for community contribution. See wiki [here](https://open-cluster-management.io/concepts/architecture/).

## Community, discussion, contribution, and support

Check the [CONTRIBUTING Doc](CONTRIBUTING.md) for how to contribute to the repo.

## License

This project is licensed under the *Apache License 2.0*. A copy of the license can be found in [LICENSE](LICENSE).


## Getting Started
The Cluster Back up and Restore Operator provides disaster recovery solutions for the case when the Red Hat Advanced Cluster Management for Kubernetes hub goes down and needs to be recreated. Scenarios outside the scope of this component : disaster recovery scenarios for applications running on managed clusters or scenarios where the managed clusters go down. 

The Cluster Back up and Restore Operator runs on the Red Hat Advanced Cluster Management for Kubernetes hub and depends on the [OADP Operator](https://github.com/openshift/oadp-operator) to create a connection to a backup storage location on the hub, which is then used to backup and restore user created hub resources. 

The Cluster Back up and Restore Operator chart is installed automatically by the MultiClusterHub resource, when installing or upgrading to version 2.5 of the Red Hat Advanced Cluster Management operator. The OADP Operator will be installed automatically with the Cluster Back up and Restore Operator chart, as a chart hook.

Before you can use the Cluster Back up and Restore operator, the [OADP Operator](https://github.com/openshift/oadp-operator/blob/master/docs/install_olm.md) must be configured to set the connection to the storage location where backups will be saved. Make sure you follow the steps to create the [secret for the cloud storage](https://github.com/openshift/oadp-operator#creating-credentials-secret) where the backups are going to be saved, then use that secret when creating the [DataProtectionApplication resource](https://github.com/openshift/oadp-operator/blob/master/docs/install_olm.md#create-the-dataprotectionapplication-custom-resource) to setup the connection to the storage location.

<b>Note</b>: The Cluster Back up and Restore Operator chart installs the [backup-restore-enabled](https://github.com/stolostron/cluster-backup-chart/blob/main/stable/cluster-backup-chart/templates/hub-backup-pod.yaml) Policy, used to inform on issues with the backup and restore component. The Policy templates check if the required pods are running, storage location is available, backups are available at the defined location and no erros status is reported by the main resources. This Policy is intended to help notify the Hub admin of any backup issues as the hub is active and expected to produce backups.



## Design

The operator defines the `BackupSchedule.cluster.open-cluster-management.io` resource, used to setup Red Hat Advanced Cluster Management for Kubernetes backup schedules, and `Restore.cluster.open-cluster-management.io` resource, used to process and restore these backups.
The operator sets the options needed to backup remote clusters identity and any other hub resources that needs to be restored.

![Cluster Backup Controller Dataflow](images/cluster-backup-controller-dataflow.png)


## What is backed up

The Cluster Back up and Restore Operator solution provides backup and restore support for all Red Hat Advanced Cluster Management for Kubernetes hub resources like managed clusters, applications, policies, bare metal assets.
It  provides support for backing up any third party resources extending the basic hub installation. 
With this backup solution, you can define a cron based backup schedule which runs at specified time intervals and continuously backs up the latest version of the hub content.
When the hub needs to be replaced or in a disaster scenario when the hub goes down, a new hub can be deployed and backed up data moved to the new hub, so that the new hub replaces the old one.

The steps below show how the Cluster Back up and Restore Operator finds the resources to be backed up.
With this approach the backup includes all CRDs installed on the hub, including any extensions using third parties components.
1. Exclude all resources in the MultiClusterHub namespace. This is to avoid backing up installation resources which are linked to the current Hub identity and should not be backed up.
2. Backup all CRDs with an api version suffixed by `.open-cluster-management.io`. This will cover all Advanced Cluster Management resources.
3. Additionally, backup all CRDs from these api groups: `argoproj.io`,`app.k8s.io`,`core.observatorium.io`,`hive.openshift.io`
4. Exclude ACM CRDs from the following api groups: `clustermanagementaddon`, `observabilityaddon`, `applicationmanager`,`certpolicycontroller`,`iampolicycontroller`,`policycontroller`,`searchcollector`,`workmanager`,`backupschedule`,`restore`,`clusterclaim.cluster.open-cluster-management.io`
5. Backup secrets and configmaps with one of the following label annotations:
`cluster.open-cluster-management.io/type`, `hive.openshift.io/secret-type`, `cluster.open-cluster-management.io/backup`
6. Use this label annotation for any other resources that should be backed up and are not included in the above criteria: `cluster.open-cluster-management.io/backup`
7. Resources picked up by the above rules that should not be backed up, can be explicitly excluded when setting this label annotation: `velero.io/exclude-from-backup=true` 



## Scheduling a cluster backup 

A backup schedule is activated when creating the `backupschedule.cluster.open-cluster-management.io` resource, as shown [here](https://github.com/stolostron/cluster-backup-operator/blob/main/config/samples/cluster_v1beta1_backupschedule.yaml)

After you create a `backupschedule.cluster.open-cluster-management.io` resource you should be able to run `oc get bsch -n <oadp-operator-ns>` and get the status of the scheduled cluster backups. The `<oadp-operator-ns>` is the namespace where BackupSchedule was created and it should be the same namespace where the OADP Operator was installed.

The `backupschedule.cluster.open-cluster-management.io` creates 6 `schedule.velero.io` resources, used to generate the backups.

Run `os get schedules -A | grep acm` to view the list of backup scheduled.

Resources are backed up in 3 separate groups:
1. credentials backup ( 3 backup files, for hive, ACM and generic backups )
2. resources backup ( 2 backup files, one for the ACM resources and second for generic resources, labeled with `cluster.open-cluster-management.io/backup`)

3. managed clusters backup, labeled with `cluster.open-cluster-management.io/backup-schedule-type: acm-managed-clusters` ( one backup containing only resources which result in activating the managed cluster connection to the hub where the backup was restored on)


<b>Note</b>:

a. The backup file created in step 2. above contains managed cluster specific resources but does not contain the subset of resources which will result in managed clusters connect to this hub. These resources, also called activation resources, are contained by the managed clusters backup, created in step 3. When you restore on a new hub just the resources from step 1 and 2 above, the new hub shows all managed clusters but they are in a detached state. The managed clusters are still connected to the original hub that had produced the backup files.

b. Only managed clusters created using the hive api will be automatically connected with the new hub when the `acm-managed-clusters` backup from step 3 is restored on another hub. All other managed clusters will show up as `Pending Import` and must be imported back on the new hub.

c. When restoring the `acm-managed-clusters` backup on a new hub, by using the `veleroManagedClustersBackupName: latest` option on the restore resource, make sure the old hub from where the backups have been created is shut down, otherwise the old hub will try to reconnect with the managed clusters as soon as the managed cluster reconciliation addons find the managed clusters are no longer available, so both hubs will try to manage the clusters at the same time.

### Backup Collisions

As hubs change from passive to primary clusters and back, different clusters can backup up data at the same storage location. This could result in backup collisions, which means the latest backups are generated by a hub who is no longer the designated primary hub. That hub produces backups because the `BackupSchedule.cluster.open-cluster-management.io` resource is still Enabled on this hub, but it should no longer write backup data since that hub is no longer a primary hub.
Situations when a backup collision could happen:
1. Primary hub goes down unexpectedly:
    - 1.1) Primary hub, Hub1, goes down 
    - 1.2) Hub1 backup data is restored on Hub2
    - 1.3) The admin creates the `BackupSchedule.cluster.open-cluster-management.io` resource on Hub2. Hub2 is now the primary hub and generates backup data to the common storage location. 
    - 1.4) Hub1 comes back to life unexpectedly. Since the `BackupSchedule.cluster.open-cluster-management.io` resource is still enabled on Hub1, it will resume writting backups to the same storage location as Hub2. Both Hub1 and Hub2 are now writting backup data at the same storage location. Any cluster restoring the latest backups from this storage location could pick up Hub1 data instead of Hub2.
2. The admin tests the disaster scenario by making Hub2 a primary hub:
    - 2.1) Hub1 is stopped
    - 2.2) Hub1 backup data is restored on Hub2
    - 2.3) The admin creates the `BackupSchedule.cluster.open-cluster-management.io` resource on Hub2. Hub2 is now the primary hub and generates backup data to the common storage location. 
    - 2.4) After the disaster test is completed, the admin will revert to the previous state and make Hub1 the primary hub:
        - 2.4.1) Hub1 is started. Hub2 is still up though and the `BackupSchedule.cluster.open-cluster-management.io` resource is Enabled on Hub2. Until Hub2 `BackupSchedule.cluster.open-cluster-management.io` resource is deleted or Hub2 is stopped, Hub2 could write backups at any time at the same storage location, corrupting the backup data. Any cluster restoring the latest backups from this location could pick up Hub2 data instead of Hub1. The right approach here would have been to first stop Hub2 or delete the `BackupSchedule.cluster.open-cluster-management.io` resource on Hub2, then start Hub1.

In order to avoid and to report this type of backup collisions, a BackupCollision state exists for a  `BackupSchedule.cluster.open-cluster-management.io` resource. The controller checks regularly if the latest backup in the storage location has been generated from the current cluster. If not, it means that another cluster has more recently written backup data to the storage location so this hub is in collision with another hub.

In this case, the current hub `BackupSchedule.cluster.open-cluster-management.io` resource status is set to BackupCollision and the `Schedule.velero.io` resources created by this resource are deleted to avoid data corruption. The BackupCollision is reported by the [backup Policy](https://github.com/stolostron/cluster-backup-chart/blob/main/stable/cluster-backup-chart/templates/hub-backup-pod.yaml). The admin should verify what hub must be the one writting data to the  storage location, than remove the `BackupSchedule.cluster.open-cluster-management.io` resource from the invalid hub and recreated a new `BackupSchedule.cluster.open-cluster-management.io` resource on the valid, primary hub, to resume the backup on this hub. 

Example of a schedule in `BackupCollision` state:

```
oc get backupschedule -A
NAMESPACE       NAME               PHASE             MESSAGE
openshift-adp   schedule-hub-1   BackupCollision   Backup acm-resources-schedule-20220301234625, from cluster with id [be97a9eb-60b8-4511-805c-298e7c0898b3] is using the same storage location. This is a backup collision with current cluster [1f30bfe5-0588-441c-889e-eaf0ae55f941] backup. Review and resolve the collision then create a new BackupSchedule resource to  resume backups from this cluster.
```

## Restoring a backup

In a usual restore scenario, the hub where the backups have been executed becomes unavailable and data backed up needs to be moved to a new hub. This is done by running the cluster restore operation on the hub where the backed up data needs to be moved to. In this case, the restore operation is executed on a different hub than the one where the backup was created. 

There are also cases where you want to restore the data on the same hub where the backup was collected, in order to recover data from a previous snapshot. In this case both restore and backup operations are executed on the same hub.

A restore backup is executed when creating the `restore.cluster.open-cluster-management.io` resource on the hub. A few samples are available [here](https://github.com/stolostron/cluster-backup-operator/tree/main/config/samples)
- use the [passive sample](https://github.com/stolostron/cluster-backup-operator/blob/main/config/samples/cluster_v1beta1_restore_passive.yaml) if you want to restore all resources on the new hub but you don't want to have the managed clusters be managed by the new hub. You can use this restore configuration when the initial hub is still up and you want to prevent the managed clusters to change ownership. You could use this restore option when you want to view the initial hub configuration on the new hub or to prepare the new hub to take over when needed; in that case just restore the managed clusters resources using the [passive activation sample](https://github.com/stolostron/cluster-backup-operator/blob/main/config/samples/cluster_v1beta1_restore_passive_activate.yaml).
- use the [passive activation sample](https://github.com/stolostron/cluster-backup-operator/blob/main/config/samples/cluster_v1beta1_restore_passive_activate.yaml) when you want for this hub to manage the clusters. In this case it is assumed that the other data has been restored already on this hub using the [passive sample](https://github.com/stolostron/cluster-backup-operator/blob/main/config/samples/cluster_v1beta1_restore_passive.yaml)
- use the [restore sample](https://github.com/stolostron/cluster-backup-operator/blob/main/config/samples/cluster_v1beta1_restore.yaml) if you want to restore all data at once and make this hub take over the managed clusters in one step.

After you create a `restore.cluster.open-cluster-management.io` resource on the hub, you should be able to run `oc get restore -n <oadp-operator-ns>` and get the status of the restore operation. You should also be able to verify on your hub that the backed up resources contained by the backup file have been created.

<b>Note:</b> 

a. The `restore.cluster.open-cluster-management.io` resource is executed once. After the restore operation is completed, if you want to run another restore operation on the same hub, you have to create a new `restore.cluster.open-cluster-management.io` resource.

b. Although you can create multiple `restore.cluster.open-cluster-management.io` resources, only one is allowed to be executing at any moment in time.

c. The restore operation allows to restore all 3 backup types created by the backup operation, although you can choose to install only a certain type (only managed clusters or only user credentials or only hub resources). 

The restore defines 3 required spec properties, defining the restore logic for the 3 type of backed up files. 
- `veleroManagedClustersBackupName` is used to define the restore option for the managed clusters. 
- `veleroCredentialsBackupName` is used to define the restore option for the user credentials. 
- `veleroResourcesBackupName` is used to define the restore option for the hub resources (applications and policies). 

The valid options for the above properties are : 
  - `latest` - restore the last available backup file for this type of backup
  - `skip` - do not attempt to restore this type of backup with the current restore operation
  - `<backup_name>` - restore the specified backup pointing to it by name

Below you can see a sample available with the operator.

```yaml
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Restore
metadata:
  name: restore-acm
spec:
  cleanupBeforeRestore: CleanupRestored
  veleroManagedClustersBackupName: latest
  veleroCredentialsBackupName: latest
  veleroResourcesBackupName: latest
```

### Cleaning up the hub before restore
Velero currently skips backed up resources if they are already installed on the hub. This limits the scenarios that can be used when restoring hub data on a new hub. Unless the new hub is not used and the restore is applied only once, the hub could not be relibly used as a passive configuration: the data on this hub is not reflective of the data available with the restored resources.

Restore limitations examples:
1. A Policy exists on the new hub, before the backup data is restored on this hub. After the restore of the backup resources, the new hub is not identical with the initial hub from where the data was restored. The Policy should not be running on the new hub since this is a Policy not available with the backup resources.
2. A Policy exists on the new hub, before the backup data is restored on this hub. The backup data contains the same Policy but in an updated configuration. Since Velero skips existing resources, the Policy will stay unchanged on the new hub, so the Policy is not the same as the one backed up on the initial hub.
3. A Policy is restored on the new hub. The primary hub keeps updating the content and the Policy content changes as well. The user reapplies the backup on the new hub, expecting to get the updated Policy. Since the Policy already exists on the hub - created by the previous restore - it will not be restored again. So the new hub has now a different configuration from the primary hub, even if the backup contains the expected updated content; that content is not updated by Velero on the new hub.

To address above limitations, when a `Restore.cluster.open-cluster-management.io` resource is created, the Cluster Back up and Restore Operator runs a prepare for restore set of steps which will clean up the hub, before Velero restore is called. 

The prepare for cleanup option uses the `cleanupBeforeRestore` property to identify the subset of objects to clean up. There are 3 options you could set for this clean up: 
- `None` : no clean up necessary, just call Velero restore. This is to be used on a brand new hub.
- `CleanupRestored` : clean up all resources created by a previous acm restore. This should be the common usage for this property. It is less intrusive then the `CleanupAll` and covers the scenario where you start with a clean hub and keep restoring resources on this hub ( limitation sample 3 above )
- `CleanupAll` : clean up all resources on the hub which could be part of an acm backup, even if they were not created as a result of a restore operation. This is to be used when extra content has been created on this hub which requires clean up ( limitation samples 1 and 2 above ). Use this option with caution though as this will cleanup resources on the hub created by the user, not by a previous backup. It is <b>strongly recommended</b> to use the `CleanupRestored` option and to refrain from manually updating hub content when the hub is designated as a passive candidate for a disaster scenario. Basically avoid getting into the situation where you have to swipe the cluster using the `CleanupAll` option; this is given as a last alternative.



## Backup validation using a Policy

The Cluster Back up and Restore Operator [chart](https://github.com/stolostron/cluster-backup-chart) installs the [backup-restore-enabled](https://github.com/stolostron/cluster-backup-chart/blob/main/stable/cluster-backup-chart/templates/hub-backup-pod.yaml) Policy, used to inform on issues with the backup and restore component. 

The Policy has a set of templates which check for the following constraints and informs when any of them are violated. 
- Backup and restore operator pod is running
- OADP operator pod is running
- velero pod is running
- a  `BackupStorageLocation.velero.io` resource was created and the status is `Available`
- `Backup.velero.io` resources are available at the location sepcified by the `BackupStorageLocation.velero.io` resource and the backups were created by the `BackupSchedule.cluster.open-cluster-management.io` resource. This validates that the backups has been executed at least once, using the Backup and restore operator.
- if a `BackupSchedule.cluster.open-cluster-management.io` exists on the current hub, the state is not `BackupCollision`. This verifies that the current hub is not in collision with any other hub when writing backup data to the storage location. For a definition of the BackupCollision state read the [Backup Collisions section](#backup-collisions) 
- if a `BackupSchedule.cluster.open-cluster-management.io` exists on the current cluster, the status is not in (Failed, or empty state). This ensures that if this cluster is the primary hub and is generating backups, the `BackupSchedule.cluster.open-cluster-management.io` status is healthy.
- if a `Restore.cluster.open-cluster-management.io` exists on the current cluster, the status is not in (Failed, or empty state). This ensures that if this cluster is the secondary hub and is restoring backups, the `Restore.cluster.open-cluster-management.io` status is healthy.

This Policy is intended to help notify the Hub admin of any backup issues as the hub is active and expected to produce or restore backups.


# Setting up Your Dev Environment

## Prerequiste Tools
- Operator SDK

## Installation

To install the Cluster Back up and Restore Operator, you can either run it outside the cluster,
for faster iteration during development, or inside the cluster.

First we require installing the Operator CRD:

```shell
make build
make install
```

Then proceed to the installation method you prefer below.

### Outside the Cluster

If you would like to run the Cluster Back up and Restore Operator outside the cluster, execute:

```shell
make run
```

### Inside the Cluster

If you would like to run the Operator inside the cluster, you'll need to build
a container image. You can use a local private registry, or host it on a public
registry service like [quay.io](https://quay.io).

1. Build your image:
    ```shell
    make docker-build IMG=<registry>/<imagename>:<tag>
    ```
1. Push the image:
    ```shell
    make docker-push IMG=<registry>/<imagename>:<tag>
    ```
1. Deploy the Operator:
    ```shell
    make deploy IMG=<registry>/<imagename>:<tag>
    ```


## Usage

Here you can find an example of a `backupschedule.cluster.open-cluster-management.io` resource definition:

```yaml
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: BackupSchedule
metadata:
  name: schedule-acm
spec:
  veleroSchedule: 0 */6 * * * # Create a backup every 6 hours
  veleroTtl: 72h # deletes scheduled backups after 72h; optional, if not specified, the maximum default value set by velero is used - 720h
```

- `veleroSchedule` is a required property and defines a cron job for scheduling the backups.

- `veleroTtl` is an optional property and defines the expiration time for a scheduled backup resource. If not specified, the maximum default value set by velero is used, which is 720h.


This is an example of a `restore.cluster.open-cluster-management.io` resource definition

```yaml
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Restore
metadata:
  name: restore-acm
spec:
  cleanupBeforeRestore: CleanupRestored
  veleroManagedClustersBackupName: latest
  veleroCredentialsBackupName: latest
  veleroResourcesBackupName: latest
```


In order to create an instance of `backupschedule.cluster.open-cluster-management.io` or `restore.cluster.open-cluster-management.io` you can start from one of the [sample configurations](config/samples).
Replace the `<oadp-operator-ns>` with the namespace name used to install the OADP Operator.


```shell
kubectl create -n <oadp-operator-ns> -f config/samples/cluster_v1beta1_backupschedule.yaml
kubectl create -n <oadp-operator-ns> -f config/samples/cluster_v1beta1_restore.yaml
```

# Testing

## Schedule  a backup 

After you create a `backupschedule.cluster.open-cluster-management.io` resource you should be able to run `oc get bsch -n <oadp-operator-ns>` and get the status of the scheduled cluster backups.

In the example below, you have created a `backupschedule.cluster.open-cluster-management.io` resource named schedule-acm.

The resource status shows the definition for the 3 `schedule.velero.io` resources created by this cluster backup scheduler. 

```
$ oc get bsch -n <oadp-operator-ns>
NAME           PHASE
schedule-acm   
```

## Restore a backup

After you create a `restore.cluster.open-cluster-management.io` resource on the new hub, you should be able to run `oc get restore -n <oadp-operator-ns>` and get the status of the restore operation. You should also be able to verify on the new hub that the backed up resources contained by the backup file have been created.

The restore defines 3 required spec properties, defining the restore logic for the 3 type of backed up files. 
- `veleroManagedClustersBackupName` is used to define the restore option for the managed clusters. 
- `veleroCredentialsBackupName` is used to define the restore option for the user credentials. 
- `veleroResourcesBackupName` is used to define the restore option for the hub resources (applications and policies). 

The valid options for the above properties are : 
  - `latest` - restore the last available backup file for this type of backup
  - `skip` - do not attempt to restore this type of backup with the current restore operation
  - `<backup_name>` - restore the specified backup pointing to it by name

The `cleanupBeforeRestore` property is used to clean up resources before the restore is executed. More details about this options [here](#cleaning-up-the-hub-before-restore).

<b>Note:</b> The `restore.cluster.open-cluster-management.io` resource is executed once. After the restore operation is completed, if you want to run another restore operation on the same hub, you have to create a new `restore.cluster.open-cluster-management.io` resource.


Below is an example of a `restore.cluster.open-cluster-management.io` resource, restoring all 3 types of backed up files, using the latest available backups:

```yaml
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Restore
metadata:
  name: restore-acm
spec:
  cleanupBeforeRestore: CleanupRestored
  veleroManagedClustersBackupName: latest
  veleroCredentialsBackupName: latest
  veleroResourcesBackupName: latest
```

You can define a restore operation where you only restore the managed clusters:

```yaml
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Restore
metadata:
  name: restore-acm
spec:
  cleanupBeforeRestore: None
  veleroManagedClustersBackupName: latest
  veleroCredentialsBackupName: skip
  veleroResourcesBackupName: skip
```

The sample below restores the managed clusters from backup `acm-managed-clusters-schedule-20210902205438` :

```yaml
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Restore
metadata:
  name: restore-acm
spec:
  cleanupBeforeRestore: None
  veleroManagedClustersBackupName: acm-managed-clusters-schedule-20210902205438
  veleroCredentialsBackupName: skip
  veleroResourcesBackupName: skip
```

