/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	civ1alpha1 "codeberg.org/thaddeuscleo/forgejo-runner-operator/api/v1alpha1"
)

const (
	CONFIG_VOLUME_NAME = "config"
)

// ForgejoRunnerReconciler reconciles a ForgejoRunner object
type ForgejoRunnerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ci.cleoverse.net,resources=forgejorunners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ci.cleoverse.net,resources=forgejorunners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ci.cleoverse.net,resources=forgejorunners/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *ForgejoRunnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	var runner civ1alpha1.ForgejoRunner

	if err := r.Get(ctx, req.NamespacedName, &runner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runner.Name,
			Namespace: runner.Namespace,
		},
	}

	var secret corev1.Secret

	ge := r.Get(
		ctx,
		client.ObjectKey{
			Namespace: runner.Namespace,
			Name:      runner.Spec.ConfigSecretRef.Name,
		},
		&secret,
	)

	if apierrors.IsNotFound(ge) {
		ge = r.setReadyCondition(
			ctx,
			&runner,
			metav1.ConditionFalse,
			"SecretNotFound",
			fmt.Sprintf(
				"Referenced Secret %q does not exist",
				runner.Spec.ConfigSecretRef.Name,
			),
		)
		if ge != nil {
			return ctrl.Result{}, ge
		}

		return ctrl.Result{
			RequeueAfter: 10 * time.Second,
		}, nil
	}

	if ge != nil {
		return ctrl.Result{}, ge
	}

	if len(secret.Data["runner-config.yml"]) == 0 {
		ge = r.setReadyCondition(
			ctx,
			&runner,
			metav1.ConditionFalse,
			"InvalidSecret",
			"Secret must contain a non-empty runner-config.yml",
		)
		if ge != nil {
			return ctrl.Result{}, ge
		}

		return ctrl.Result{
			RequeueAfter: 10 * time.Second,
		}, nil
	}

	_, err := controllerutil.CreateOrUpdate(
		ctx, r.Client, deployment,
		func() error {
			labels := map[string]string{
				"app.kubernetes.io/name":     "forgejo-runner",
				"app.kubernetes.io/instance": runner.Name,
			}

			if deployment.CreationTimestamp.IsZero() {
				deployment.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: labels,
				}
				deployment.Spec.Template.Labels = labels
				deployment.Spec.Template.Spec = runnerPodSpec(&runner)
			} else if !metav1.IsControlledBy(deployment, &runner) {
				return fmt.Errorf(
					"deployment %s is not controlled by ForgejoRunner",
					deployment.Name,
				)
			}

			replicas := int32(1)
			deployment.Spec.Replicas = &replicas

			foundContainer := false
			for i := range deployment.Spec.Template.Spec.Containers {
				container := &deployment.Spec.Template.Spec.Containers[i]
				if container.Name == "runner" {
					container.Image = runner.Spec.Image
					foundContainer = true
				}
			}
			if !foundContainer {
				return fmt.Errorf("runner container is missing")
			}

			foundVolume := false
			for i := range deployment.Spec.Template.Spec.Volumes {
				volume := &deployment.Spec.Template.Spec.Volumes[i]
				if volume.Name == CONFIG_VOLUME_NAME && volume.Secret != nil {
					volume.Secret.SecretName = runner.Spec.ConfigSecretRef.Name
					foundVolume = true
				}
			}
			if !foundVolume {
				return fmt.Errorf("config volume is missing")
			}

			return controllerutil.SetControllerReference(
				&runner, deployment, r.Scheme,
			)
		},
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	before := runner.DeepCopy()

	ready := deployment.Status.AvailableReplicas >= 1 &&
		deployment.Status.ObservedGeneration >= deployment.Generation

	runner.Status.ObservedGeneration = runner.Generation
	runner.Status.ReadyReplicas = deployment.Status.AvailableReplicas

	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "DeploymentNotReady",
		Message:            "Waiting for the runner Deployment",
		ObservedGeneration: runner.Generation,
	}

	if ready {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "DeploymentAvailable"
		condition.Message = "Runner Deployment has an available Pod"
	}

	meta.SetStatusCondition(&runner.Status.Conditions, condition)

	if !reflect.DeepEqual(before.Status, runner.Status) {
		if err := r.Status().Update(ctx, &runner); err != nil {
			return ctrl.Result{}, err
		}
	}

	if !ready {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (r *ForgejoRunnerReconciler) setReadyCondition(
	ctx context.Context,
	runner *civ1alpha1.ForgejoRunner,
	status metav1.ConditionStatus,
	reason string,
	message string,
) error {
	before := runner.DeepCopy()

	runner.Status.ObservedGeneration = runner.Generation

	meta.SetStatusCondition(
		&runner.Status.Conditions,
		metav1.Condition{
			Type:               "Ready",
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: runner.Generation,
		},
	)

	if reflect.DeepEqual(before.Status, runner.Status) {
		return nil
	}

	return r.Status().Patch(
		ctx,
		runner,
		client.MergeFrom(before),
	)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ForgejoRunnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&civ1alpha1.ForgejoRunner{}).
		Owns(&appsv1.Deployment{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(
				r.mapSecretToRunners,
			),
		).
		Named("forgejorunner").
		Complete(r)

}

func (r *ForgejoRunnerReconciler) mapSecretToRunners(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	var runners civ1alpha1.ForgejoRunnerList

	err := r.List(
		ctx,
		&runners,
		client.InNamespace(obj.GetNamespace()),
	)
	if err != nil {
		logf.FromContext(ctx).Error(
			err,
			"Failed to list ForgejoRunners",
		)
		return nil
	}

	requests := make([]reconcile.Request, 0)

	for _, runner := range runners.Items {
		if runner.Spec.ConfigSecretRef.Name != obj.GetName() {
			continue
		}

		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      runner.Name,
				Namespace: runner.Namespace,
			},
		})
	}

	return requests
}
