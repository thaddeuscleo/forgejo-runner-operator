package controller

import (
	"context"

	civ1alpha1 "codeberg.org/thaddeuscleo/forgejo-runner-operator/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("ForgejoRunner Controller", func() {
	var (
		ctx        context.Context
		namespace  *corev1.Namespace
		runner     *civ1alpha1.ForgejoRunner
		reconciler *ForgejoRunnerReconciler
		key        types.NamespacedName

		reconcileOnce func()
		createSecret  func() *corev1.Secret
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Use a unique namespace for each test.
		namespace = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "forgejo-test-",
			},
		}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		runner = &civ1alpha1.ForgejoRunner{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sample",
				Namespace: namespace.Name,
			},
			Spec: civ1alpha1.ForgejoRunnerSpec{
				Image: "data.forgejo.org/forgejo/runner:13",
				ConfigSecretRef: corev1.LocalObjectReference{
					Name: "forgejo-runner-config",
				},
			},
		}
		Expect(k8sClient.Create(ctx, runner)).To(Succeed())

		key = types.NamespacedName{
			Name:      runner.Name,
			Namespace: runner.Namespace,
		}

		reconciler = &ForgejoRunnerReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		reconcileOnce = func() {
			_, err := reconciler.Reconcile(
				ctx,
				ctrl.Request{NamespacedName: key},
			)
			Expect(err).NotTo(HaveOccurred())
		}

		createSecret = func() *corev1.Secret {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "forgejo-runner-config",
					Namespace: namespace.Name,
				},
				Data: map[string][]byte{
					"runner-config.yml": []byte("runner: {}\n"),
				},
			}

			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			return secret
		}
	})

	It("creates a managed Deployment", func() {
		createSecret()
		reconcileOnce()

		var deployment appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &deployment)).To(Succeed())

		Expect(deployment.Spec.Replicas).NotTo(BeNil())
		Expect(*deployment.Spec.Replicas).To(Equal(int32(1)))

		Expect(
			deployment.Spec.Template.Spec.Containers[0].Image,
		).To(Equal(runner.Spec.Image))

		Expect(metav1.IsControlledBy(&deployment, runner)).
			To(BeTrue())
	})

	It("corrects Deployment drift", func() {
		createSecret()
		reconcileOnce()

		var deployment appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &deployment)).To(Succeed())

		// Simulate someone manually modifying the Deployment.
		deployment.Spec.Template.Spec.Containers[0].Image =
			"nginx:latest"

		replicas := int32(0)
		deployment.Spec.Replicas = &replicas

		Expect(k8sClient.Update(ctx, &deployment)).To(Succeed())

		reconcileOnce()

		Expect(k8sClient.Get(ctx, key, &deployment)).To(Succeed())

		Expect(
			deployment.Spec.Template.Spec.Containers[0].Image,
		).To(Equal(runner.Spec.Image))

		Expect(*deployment.Spec.Replicas).To(Equal(int32(1)))
	})

	It("reports SecretNotFound", func() {
		reconcileOnce()

		var current civ1alpha1.ForgejoRunner
		Expect(k8sClient.Get(ctx, key, &current)).To(Succeed())

		condition := meta.FindStatusCondition(
			current.Status.Conditions, "Ready",
		)

		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal("SecretNotFound"))
	})

	It("reports InvalidSecret", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "forgejo-runner-config",
				Namespace: namespace.Name,
			},
			Data: map[string][]byte{},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		reconcileOnce()

		var current civ1alpha1.ForgejoRunner
		Expect(k8sClient.Get(ctx, key, &current)).To(Succeed())

		condition := meta.FindStatusCondition(
			current.Status.Conditions, "Ready",
		)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Reason).To(Equal("InvalidSecret"))
	})

	It("recovers after Secret restoration", func() {
		secret := createSecret()
		reconcileOnce()

		// envtest has no Deployment controller.
		// Simulate Kubernetes marking the Deployment available.
		var deployment appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &deployment)).To(Succeed())

		deployment.Status.Replicas = 1
		deployment.Status.ReadyReplicas = 1
		deployment.Status.AvailableReplicas = 1
		deployment.Status.UpdatedReplicas = 1
		deployment.Status.ObservedGeneration = deployment.Generation

		Expect(k8sClient.Status().Update(ctx, &deployment)).
			To(Succeed())

		reconcileOnce()

		var current civ1alpha1.ForgejoRunner
		Expect(k8sClient.Get(ctx, key, &current)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(
			current.Status.Conditions, "Ready",
		)).To(BeTrue())

		// Delete the dependency.
		Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		reconcileOnce()

		Expect(k8sClient.Get(ctx, key, &current)).To(Succeed())
		condition := meta.FindStatusCondition(
			current.Status.Conditions, "Ready",
		)
		Expect(condition.Reason).To(Equal("SecretNotFound"))

		// Restore the dependency.
		createSecret()
		reconcileOnce()

		Expect(k8sClient.Get(ctx, key, &current)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(
			current.Status.Conditions, "Ready",
		)).To(BeTrue())
	})

	It("does not update Deployment when desired state is unchanged", func() {
		// Arrange: Create dependency and reconcile once.
		createSecret()
		reconcileOnce()

		var before appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &before)).To(Succeed())

		originalVersion := before.ResourceVersion
		originalGeneration := before.Generation

		// Act: Run reconciliation repeatedly.
		for range 5 {
			reconcileOnce()
		}

		// Assert: Deployment was not modified.
		var after appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &after)).To(Succeed())

		Expect(after.ResourceVersion).
			To(Equal(originalVersion))

		Expect(after.Generation).
			To(Equal(originalGeneration))

		Expect(after.Spec).
			To(Equal(before.Spec))
	})

	It("does not adopt an unrelated Deployment", func() {
		createSecret()

		// Arrange: A Deployment already exists
		// with the same name as ForgejoRunner.
		replicas := int32(2)

		labels := map[string]string{
			"app": "unrelated",
		}

		foreignDeployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: labels,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: labels,
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "unrelated-app",
								Image: "nginx:latest",
							},
						},
					},
				},
			},
		}

		Expect(k8sClient.Create(
			ctx, foreignDeployment,
		)).To(Succeed())

		var before appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &before)).
			To(Succeed())

		// Act: Attempt to reconcile ForgejoRunner.
		_, err := reconciler.Reconcile(
			ctx,
			ctrl.Request{
				NamespacedName: key,
			},
		)

		// Assert: Reconciliation must fail.
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(
			"not controlled by ForgejoRunner",
		))

		// Assert: Unrelated Deployment remains intact.
		var after appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &after)).
			To(Succeed())

		Expect(after.ResourceVersion).
			To(Equal(before.ResourceVersion))

		Expect(after.Spec).
			To(Equal(before.Spec))

		Expect(after.OwnerReferences).
			To(BeEmpty())
	})
})
