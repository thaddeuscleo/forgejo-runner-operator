package controller

import (
	civ1alpha1 "codeberg.org/thaddeuscleo/forgejo-runner-operator/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
)

func runnerPodSpec(runner *civ1alpha1.ForgejoRunner) corev1.PodSpec {
	uid := int64(1000)

	return corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:  &uid,
			RunAsGroup: &uid,
			FSGroup:    &uid,
		},
		Containers: []corev1.Container{
			{
				Name:  "runner",
				Image: runner.Spec.Image,
				Args: []string{
					"forgejo-runner",
					"daemon",
					"--config",
					"/etc/forgejo/runner-config.yml",
				},
				WorkingDir: "/data",
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "config",
						MountPath: "/etc/forgejo",
						ReadOnly:  true,
					},
					{
						Name:      "work",
						MountPath: "/data",
					},
				},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: runner.Spec.ConfigSecretRef.Name,
					},
				},
			},
			{
				Name: "work",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
	}
}
