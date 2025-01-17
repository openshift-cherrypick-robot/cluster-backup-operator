/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	v1beta1 "github.com/open-cluster-management/cluster-backup-operator/api/v1beta1"
	chnv1 "github.com/open-cluster-management/multicloud-operators-channel/pkg/apis/apps/v1"
	veleroapi "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	// include resources from these api groups
	includedAPIGroupsSuffix = []string{
		".open-cluster-management.io",
	}
	includedAPIGroupsByName = []string{
		"argoproj.io",
		"app.k8s.io",
		"core.observatorium.io",
		"hive.openshift.io",
	}

	// exclude resources from these api groups
	excludedAPIGroups = []string{
		"admission.cluster.open-cluster-management.io",
		"admission.work.open-cluster-management.io",
		"internal.open-cluster-management.io",
		"operator.open-cluster-management.io",
		"work.open-cluster-management.io",
	}
	// exclude these CRDs
	// they are part of the included api groups but are either not needed
	// or they are being recreated by owner resources, which are also backed up
	excludedCRDs = []string{
		"clustermanagementaddon",
		"applicationmanager",
		"certpolicycontroller",
		"iampolicycontroller",
		"policycontroller",
		"searchcollector",
		"workmanager",
		"backupschedule",
		"restore",
	}

	// resources used to activate the connection between hub and managed clusters - activation resources
	backupManagedClusterResources = []string{
		"managedcluster", //global
		"klusterletaddonconfig",
		"managedclusteraddon",
		"managedclusterset",
		"managecclustersetbindings",
		"clusterpool",
		"clusterclaim",
		"clustercurator",
		"managedclusterview",
		"clusterstatus",
	}

	// all backup resources, except secrets, configmaps and managed cluster activation resources
	// backup resources will be generated from the api groups CRDs
	backupResources = []string{
		"clusterdeployment",
		"machinepool",
	}

	backupCredsResources = []string{
		"secret",
	}

	// secrets and configmaps labels
	backupCredsUserLabel    = "cluster.open-cluster-management.io/type"   // #nosec G101 -- This is a false positive
	backupCredsHiveLabel    = "hive.openshift.io/secret-type"             // hive
	backupCredsClusterLabel = "cluster.open-cluster-management.io/backup" // #nosec G101 -- This is a false positive
)

var (
	apiGVString = v1beta1.GroupVersion.String()
	// create credentials schedule first since this is the fastest one, followed by resources
	// mapping ResourceTypes to Velero schedule names
	veleroScheduleNames = map[ResourceType]string{
		Credentials:        "acm-credentials-schedule",
		CredentialsHive:    "acm-credentials-hive-schedule",
		CredentialsCluster: "acm-credentials-cluster-schedule",
		Resources:          "acm-resources-schedule",
		ResourcesGeneric:   "acm-resources-generic-schedule",
		ManagedClusters:    "acm-managed-clusters-schedule",
	}
)

// clean up old backups if they exceed the maxCount number
func cleanupBackups(
	ctx context.Context,
	maxBackups int,
	c client.Client,
) {
	backupLogger := log.FromContext(ctx)

	backupLogger.Info(fmt.Sprintf("check if needed to remove backups maxBackups=%d", maxBackups))
	veleroBackupList := veleroapi.BackupList{}
	if err := c.List(ctx, &veleroBackupList, &client.ListOptions{}); err != nil {
		backupLogger.Error(err, "failed to get veleroapi.BackupList")
	} else {

		// get only acm resources backups and not in deleting state
		// which are backups starting with acm-resources-schedule
		sliceBackups := filterBackups(veleroBackupList.Items[:], func(bkp veleroapi.Backup) bool {
			return strings.HasPrefix(bkp.Name, veleroScheduleNames[Resources]) &&
				bkp.Status.Phase != veleroapi.BackupPhaseDeleting
		})

		if maxBackups < len(sliceBackups) {
			// need to delete backups
			// sort backups by create time
			sort.Slice(sliceBackups, func(i, j int) bool {
				var timeA int64
				var timeB int64
				if sliceBackups[i].Status.StartTimestamp != nil {
					timeA = sliceBackups[i].Status.StartTimestamp.Time.Unix()
				}
				if sliceBackups[j].Status.StartTimestamp != nil {
					timeB = sliceBackups[j].Status.StartTimestamp.Time.Unix()
				}
				return timeA < timeB
			})

			for i := 0; i < len(sliceBackups)-maxBackups; i++ {

				// for each resources backup find all corresponding backups
				// with the creation timestamp in the +- 2s interval and remove them
				resourcesBackup := &sliceBackups[i]
				creationTimestamp := resourcesBackup.CreationTimestamp
				relatedBackups := filterBackups(veleroBackupList.Items[:], func(bkp veleroapi.Backup) bool {
					isRelated := false
					if creationTimestamp.Sub(bkp.CreationTimestamp.Time).Seconds() > 2 ||
						bkp.CreationTimestamp.Sub(creationTimestamp.Time) > 2 {
						return isRelated // not related, more then 2s appart
					}

					// check if the backup name is in the list of acm backups
					for key := range veleroScheduleNames {
						if strings.HasPrefix(bkp.Name, veleroScheduleNames[key]) {
							isRelated = true
							break
						}
					}

					return isRelated

				})
				// delete all related backups with the same timestamp
				for i := range relatedBackups {
					deleteBackup(ctx, &relatedBackups[i], c)
				}
			}
		}

	}
}

func deleteBackup(
	ctx context.Context,
	backup *veleroapi.Backup,
	c client.Client,
) {
	// delete backup now
	backupLogger := log.FromContext(ctx)
	backupName := backup.ObjectMeta.Name
	backupNamespace := backup.ObjectMeta.Namespace
	backupLogger.Info(fmt.Sprintf("delete backup %s", backupName))

	backupDeleteIdentity := types.NamespacedName{
		Name:      backupName,
		Namespace: backupNamespace,
	}

	// get the velero CR using the backupDeleteIdentity
	veleroDeleteBackup := &veleroapi.DeleteBackupRequest{}
	err := c.Get(ctx, backupDeleteIdentity, veleroDeleteBackup)
	if err != nil {
		// check if this is a  resource NotFound error, in which case create the resource
		if k8serr.IsNotFound(err) {

			veleroDeleteBackup.Spec.BackupName = backupName
			veleroDeleteBackup.Name = backupDeleteIdentity.Name
			veleroDeleteBackup.Namespace = backupDeleteIdentity.Namespace

			err = c.Create(ctx, veleroDeleteBackup, &client.CreateOptions{})
			if err != nil {
				backupLogger.Error(
					err,
					fmt.Sprintf("create  DeleteBackupRequest request error for %s", backupName),
				)
			}
		} else {
			backupLogger.Error(err, fmt.Sprintf("Failed to create DeleteBackupRequest for resource %s", backupName))
		}
	} else {
		backupLogger.Info(fmt.Sprintf("DeleteBackupRequest already exists, skip request creation %s", backupName))
	}
}

// set all acm resources backup info
func setResourcesBackupInfo(
	ctx context.Context,
	veleroBackupTemplate *veleroapi.BackupSpec,
	resourcesToBackup []string,
	c client.Client,
) {

	backupLogger := log.FromContext(ctx)
	var clusterResource bool = true
	veleroBackupTemplate.IncludeClusterResources = &clusterResource
	veleroBackupTemplate.ExcludedNamespaces = appendUnique(
		veleroBackupTemplate.ExcludedNamespaces,
		"local-cluster",
	)

	for i := range resourcesToBackup { // acm resources
		veleroBackupTemplate.IncludedResources = appendUnique(
			veleroBackupTemplate.IncludedResources,
			resourcesToBackup[i],
		)
	}

	// exclude acm channel namespaces
	channels := chnv1.ChannelList{}
	if err := c.List(ctx, &channels, &client.ListOptions{}); err != nil {
		backupLogger.Error(err, "failed to get chnv1.ChannelList")
	} else {
		for i := range channels.Items {
			if channels.Items[i].Name == "charts-v1" {
				veleroBackupTemplate.ExcludedNamespaces = appendUnique(
					veleroBackupTemplate.ExcludedNamespaces,
					channels.Items[i].Namespace,
				)
			}
		}
	}

}

// set credentials backup info
func setGenericResourcesBackupInfo(
	ctx context.Context,
	veleroBackupTemplate *veleroapi.BackupSpec,
	resourcesAlreadyBackedup []string,
	c client.Client,
) {

	var clusterResource bool = true // check global resources
	veleroBackupTemplate.IncludeClusterResources = &clusterResource

	for i := range resourcesAlreadyBackedup { // exclude resources already backed up resources backup
		veleroBackupTemplate.ExcludedResources = appendUnique(
			veleroBackupTemplate.ExcludedResources,
			resourcesAlreadyBackedup[i],
		)
	}

	for i := range backupCredsResources { // exclude resources already backed up by creds
		veleroBackupTemplate.ExcludedResources = appendUnique(
			veleroBackupTemplate.ExcludedResources,
			backupCredsResources[i],
		)
	}

	for i := range backupManagedClusterResources { // exclude resources in managed clusters
		veleroBackupTemplate.ExcludedResources = appendUnique(
			veleroBackupTemplate.ExcludedResources,
			backupManagedClusterResources[i],
		)
	}

	if veleroBackupTemplate.LabelSelector == nil {
		labels := &v1.LabelSelector{}
		veleroBackupTemplate.LabelSelector = labels

		requirements := make([]v1.LabelSelectorRequirement, 0)
		veleroBackupTemplate.LabelSelector.MatchExpressions = requirements
	}
	req := &v1.LabelSelectorRequirement{}
	req.Key = backupCredsClusterLabel
	req.Operator = "Exists"
	veleroBackupTemplate.LabelSelector.MatchExpressions = append(
		veleroBackupTemplate.LabelSelector.MatchExpressions,
		*req,
	)
}

// set credentials backup info
func setCredsBackupInfo(
	ctx context.Context,
	veleroBackupTemplate *veleroapi.BackupSpec,
	c client.Client,
	credentialType string,
) {

	var labelKey string
	switch credentialType {
	case string(HiveSecret):
		labelKey = backupCredsHiveLabel
	case string(ClusterSecret):
		labelKey = backupCredsClusterLabel
	default:
		labelKey = backupCredsUserLabel
	}

	var clusterResource bool = false
	veleroBackupTemplate.IncludeClusterResources = &clusterResource

	for i := range backupCredsResources { // acm secrets
		veleroBackupTemplate.IncludedResources = appendUnique(
			veleroBackupTemplate.IncludedResources,
			backupCredsResources[i],
		)
	}

	if veleroBackupTemplate.LabelSelector == nil {
		labels := &v1.LabelSelector{}
		veleroBackupTemplate.LabelSelector = labels

		requirements := make([]v1.LabelSelectorRequirement, 0)
		veleroBackupTemplate.LabelSelector.MatchExpressions = requirements
	}
	req := &v1.LabelSelectorRequirement{}
	req.Key = labelKey
	req.Operator = "Exists"
	veleroBackupTemplate.LabelSelector.MatchExpressions = append(
		veleroBackupTemplate.LabelSelector.MatchExpressions,
		*req,
	)
}

// set managed clusters backup info
func setManagedClustersBackupInfo(
	ctx context.Context,
	veleroBackupTemplate *veleroapi.BackupSpec,
	c client.Client,
) {
	var clusterResource bool = true // include cluster level resources
	veleroBackupTemplate.IncludeClusterResources = &clusterResource

	for i := range backupManagedClusterResources { // managed clusters required resources, from namespace or cluster level
		veleroBackupTemplate.IncludedResources = appendUnique(
			veleroBackupTemplate.IncludedResources,
			backupManagedClusterResources[i],
		)
	}
}

func isBackupFinished(backups []*veleroapi.Backup) bool {

	if backups == nil || len(backups) <= 0 {
		return false
	}

	// get all backups and check status for each
	for i := 0; i < len(backups); i++ {
		if backups[i].Status.Phase != "Completed" &&
			backups[i].Status.Phase != "Failed" &&
			backups[i].Status.Phase != "PartiallyFailed" {
			return false // some backup is not ready
		}
	}

	return true
}

// filter backup list based on a boolean function
func filterBackups(vs []veleroapi.Backup, f func(veleroapi.Backup) bool) []veleroapi.Backup {
	filtered := make([]veleroapi.Backup, 0)
	for _, v := range vs {
		if f(v) {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

// get server resources that needs backup
func getResourcesToBackup(
	ctx context.Context,
	dc discovery.DiscoveryInterface,
) ([]string, error) {

	backupLogger := log.FromContext(ctx)

	backupResourceNames := backupResources

	// build the list of excluded resources
	ignoreCRDs := excludedCRDs

	groupList, err := dc.ServerGroups()
	if err != nil {
		return backupResourceNames, fmt.Errorf("failed to get server groups: %v", err)
	}
	if groupList == nil {
		return backupResourceNames, nil
	}
	for _, group := range groupList.Groups {

		if !shouldBackupAPIGroup(group.Name) {
			// ignore excluded api groups
			continue
		}

		for _, version := range group.Versions {
			//get all resources for each group version
			resourceList, err := dc.ServerResourcesForGroupVersion(version.GroupVersion)
			if err != nil {
				backupLogger.Error(err, "failed to get server resources")
				continue
			}
			if resourceList == nil {
				continue
			}
			for _, resource := range resourceList.APIResources {
				resourceKind := strings.ToLower(resource.Kind)
				resourceName := resourceKind + "." + group.Name
				// if resource kind is not ignored
				// and kind.group is not used to identify resource to ignore
				// the resource is not in cluster activation backup group
				// add it to the generic backup resources
				if !findValue(ignoreCRDs, resourceKind) &&
					!findValue(ignoreCRDs, resourceName) &&
					!findValue(backupManagedClusterResources, resourceKind) &&
					!findValue(backupManagedClusterResources, resourceName) {
					backupResourceNames = appendUnique(backupResourceNames, resourceName)
				}
			}
		}
	}
	return backupResourceNames, nil
}

// returns true if this api group needs to be backed up
func shouldBackupAPIGroup(groupStr string) bool {

	_, ok := find(excludedAPIGroups, groupStr)
	if ok {
		// this has to be excluded
		return false
	}

	_, ok = find(includedAPIGroupsByName, groupStr)
	// if not in the included api groups
	if !ok {
		// check if is in the included api groups by suffix
		_, ok = findSuffix(includedAPIGroupsSuffix, groupStr)

	}

	return ok
}
