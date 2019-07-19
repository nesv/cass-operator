package reconciliation

//
// This module defines constructors for k8s objects
//

import (
	"fmt"

	datastaxv1alpha1 "github.com/riptano/dse-operator/operator/pkg/apis/datastax/v1alpha1"
	"github.com/riptano/dse-operator/operator/pkg/dsereconciliation"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Creates a headless service object for the DSE Datacenter.
func newServiceForDseDatacenter(
	dseDatacenter *datastaxv1alpha1.DseDatacenter) *corev1.Service {
	// TODO adjust labels
	labels := dseDatacenter.GetDatacenterLabels()
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dseDatacenter.Spec.ClusterName + "-" + dseDatacenter.Name + "-service",
			Namespace: dseDatacenter.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			// This MUST match a template pod label in the statefulset
			Selector:  labels,
			Type:      "ClusterIP",
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				// Note: Port Names cannot be more than 15 characters
				{
					Name:       "native",
					Port:       9042,
					TargetPort: intstr.FromInt(9042),
				},
				{
					Name:       "inter-node-msg",
					Port:       8609,
					TargetPort: intstr.FromInt(8609),
				},
				{
					Name:       "intra-node",
					Port:       7000,
					TargetPort: intstr.FromInt(7000),
				},
				{
					Name:       "tls-intra-node",
					Port:       7001,
					TargetPort: intstr.FromInt(7001),
				},
				{
					Name:       "mgmt-api",
					Port:       8080,
					TargetPort: intstr.FromInt(8080),
				},
				// TODO Lococo suggests we can parse the config and see if this needs to be open
				{
					Name:       "prometheus",
					Port:       9103,
					TargetPort: intstr.FromInt(9103),
				},
				// jmx port 7199 was here, seems like we no longer need to expose it
			},
		},
	}
}

// newSeedServiceForDseDatacenter creates a headless service owned by the DseDatacenter which will attach to all seed
// nodes in the cluster
func newSeedServiceForDseDatacenter(
	dseDatacenter *datastaxv1alpha1.DseDatacenter) *corev1.Service {
	// TODO adjust labels
	labels := dseDatacenter.GetClusterLabels()
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dseDatacenter.Spec.ClusterName + "-seed-service",
			Namespace: dseDatacenter.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: nil,
			// This MUST match a template pod label in the statefulset
			Selector:  labels,
			ClusterIP: "None",
			Type:      "ClusterIP",
			// Make sure addresses are provided from the beginning so that we don't have to go back and reload seeds
			PublishNotReadyAddresses: true,
			SessionAffinityConfig: &corev1.SessionAffinityConfig{
				ClientIP: &corev1.ClientIPConfig{
					TimeoutSeconds: nil,
				},
			},
		},
	}
}

// Create a statefulset object for the DSE Datacenter.
func newStatefulSetForDseDatacenter(
	rackName string,
	dseDatacenter *datastaxv1alpha1.DseDatacenter,
	replicaCount int) (*appsv1.StatefulSet, error) {
	replicaCountInt32 := int32(replicaCount)
	labels := dseDatacenter.GetRackLabels(rackName)
	dseVersion := dseDatacenter.GetDseVersion()
	var userID int64 = 999
	var volumeCaimTemplates []corev1.PersistentVolumeClaim
	var dseVolumeMounts []corev1.VolumeMount

	dseConfigVolumeMount := corev1.VolumeMount{
		Name:      "dse-config",
		MountPath: "/config",
	}

	dseVolumeMounts = append(dseVolumeMounts, dseConfigVolumeMount)

	configData, err := dseDatacenter.GetConfigAsJSON()
	if err != nil {
		return nil, err
	}

	// Add storage if storage claim defined
	if nil != dseDatacenter.Spec.StorageClaim {
		pvName := "dse-data"
		storageClaim := dseDatacenter.Spec.StorageClaim
		dseVolumeMounts = append(dseVolumeMounts, corev1.VolumeMount{
			Name:      pvName,
			MountPath: "/var/lib/cassandra",
		})
		volumeCaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
				Name:   pvName,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources:        storageClaim.Resources,
				StorageClassName: &(storageClaim.StorageClassName),
			},
		}}
	}

	result := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dseDatacenter.Spec.ClusterName + "-" + dseDatacenter.Name + "-" + rackName + "-sts",
			Namespace: dseDatacenter.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			// TODO adjust this
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas:            &replicaCountInt32,
			ServiceName:         dseDatacenter.Spec.ClusterName + "-" + dseDatacenter.Name + "-service",
			PodManagementPolicy: appsv1.OrderedReadyPodManagement,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					// Keep the pods of a statefulset within the same zone but off the same node. We don't want multiple pods
					// on the same node just in case something goes wrong but we do want to contain all pods within a statefulset
					// to the same zone to limit the need for cross zone communication.
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
								Weight: 100,
								PodAffinityTerm: corev1.PodAffinityTerm{
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											datastaxv1alpha1.RACK_LABEL: rackName,
										},
									},
									TopologyKey: "failure-domain.beta.kubernetes.io/zone",
								},
							}},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
								Weight: 90,
								PodAffinityTerm: corev1.PodAffinityTerm{
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											datastaxv1alpha1.DATACENTER_LABEL: dseDatacenter.Name,
										},
									},
									TopologyKey: "kubernetes.io/hostname",
								},
							}},
						},
					},
					// workaround for https://cloud.google.com/kubernetes-engine/docs/security-bulletins#may-31-2019
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  &userID,
						RunAsGroup: &userID,
						FSGroup:    &userID,
					},
					Volumes: []corev1.Volume{
						{
							Name: "dse-config",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
					InitContainers: []corev1.Container{{
						Name:  "dse-config-init",
						Image: "datastax-docker.jfrog.io/datastax/dse-server-config-builder:7.0.0-3e8847c",
						VolumeMounts: []corev1.VolumeMount{
							dseConfigVolumeMount,
						},
						Env: []corev1.EnvVar{
							{
								Name:  "CONFIG_FILE_DATA",
								Value: configData,
							},
							{
								Name: "POD_IP",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "status.podIP",
									},
								},
							},
							{
								Name: "RACK_NAME",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: fmt.Sprintf("metadata.labels['%s']", datastaxv1alpha1.RACK_LABEL),
									},
								},
							},
							{
								Name:  "DSE_VERSION",
								Value: dseVersion,
							},
						},
					}},
					ServiceAccountName: "dse-operator",
					Containers: []corev1.Container{{
						// TODO FIXME
						Name:  "dse",
						Image: dseDatacenter.GetServerImage(),
						Env: []corev1.EnvVar{
							{
								Name:  "DS_LICENSE",
								Value: "accept",
							},
							{
								Name:  "DSE_AUTO_CONF_OFF",
								Value: "all",
							},
						},
						Ports: []corev1.ContainerPort{
							// Note: Port Names cannot be more than 15 characters
							// TODO: Use specified config to determine which
							//       ports we need.
							{
								Name:          "native",
								ContainerPort: 9042,
							},
							{
								Name:          "inter-node-msg",
								ContainerPort: 8609,
							},
							{
								Name:          "intra-node",
								ContainerPort: 7000,
							},
							{
								Name:          "tls-intra-node",
								ContainerPort: 7001,
							},
							{
								Name:          "mgmt-api-http",
								ContainerPort: 8080,
							},
							// jmx port 7199 was here, seems like we no longer need to expose it

						},
						LivenessProbe: &corev1.Probe{
							Handler: corev1.Handler{
								HTTPGet: &corev1.HTTPGetAction{
									Port: intstr.FromInt(8080),
									Path: "/api/v0/probes/liveness",
								},
							},
							// TODO expose config for these?
							InitialDelaySeconds: 15,
							PeriodSeconds:       15,
						},
						ReadinessProbe: &corev1.Probe{
							Handler: corev1.Handler{
								HTTPGet: &corev1.HTTPGetAction{
									Port: intstr.FromInt(8080),
									Path: "/api/v0/probes/readiness",
								},
							},
							// TODO expose config for these?
							InitialDelaySeconds: 20,
							PeriodSeconds:       10,
						},
						VolumeMounts: dseVolumeMounts,
					}},
					// TODO We can document that the user installing the operator put the imagePullSecret
					// into the service account, and the process for that is documented here:
					// https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/#add-imagepullsecrets-to-a-service-account
					// ImagePullSecrets: []corev1.LocalObjectReference{{}},
				},
			},
			VolumeClaimTemplates: volumeCaimTemplates,
		},
	}

	return result, nil
}

// Create a statefulset object for the DSE Datacenter.
func newPodDisruptionBudgetForStatefulSet(
	dseDatacenter *datastaxv1alpha1.DseDatacenter,
	statefulSet *appsv1.StatefulSet) *policyv1beta1.PodDisruptionBudget {
	// Right now, we will just have maxUnavailable at 1
	maxUnavailable := intstr.FromInt(1)
	labels := dseDatacenter.GetDatacenterLabels()
	return &policyv1beta1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSet.Name + "-pdb",
			Namespace: statefulSet.Namespace,
			Labels:    labels,
		},
		Spec: policyv1beta1.PodDisruptionBudgetSpec{
			// TODO figure selector policy this out
			// right now this is matching ALL pods for a given datacenter
			// across all racks
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			MaxUnavailable: &maxUnavailable,
		},
	}
}

// this type exists so there's no chance of pushing random strings to our progress label
type dseOperatorStatus string

const (
	updating dseOperatorStatus = "Updating"
	ready    dseOperatorStatus = "Ready"
)

func addOperatorProgressLabel(
	rc *dsereconciliation.ReconciliationContext,
	status dseOperatorStatus) error {

	labelVal := string(status)

	if rc.DseDatacenter.Spec.Labels == nil {
		rc.DseDatacenter.Spec.Labels = make(map[string]string)
	}

	existingLabel, ok := rc.DseDatacenter.Spec.Labels[datastaxv1alpha1.DseOperatorProgressLabel]
	if ok && existingLabel == labelVal {
		// early return, no need to ping k8s
		return nil
	}

	// set the label and push it to k8s
	rc.DseDatacenter.Spec.Labels[datastaxv1alpha1.DseOperatorProgressLabel] = labelVal
	err := rc.Client.Update(rc.Ctx, rc.DseDatacenter)
	if err != nil {
		rc.ReqLogger.Error(err, "error updating label",
			"label", datastaxv1alpha1.DseOperatorProgressLabel,
			"value", labelVal)
	}

	return err
}
