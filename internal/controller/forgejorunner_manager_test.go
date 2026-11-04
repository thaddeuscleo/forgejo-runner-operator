package controller

import (
	"context"
	"time"

	civ1alpha1 "codeberg.org/thaddeuscleo/forgejo-runner-operator/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("ForgejoRunner Manager", func() {
	It("reconciles events automatically", func() {
		ctx := context.Background()

		// 1. Isolate this test in its own namespace.
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "forgejo-manager-",
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		// 2. Create the actual controller-runtime Manager.
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme.Scheme,
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			HealthProbeBindAddress: "0",
			Cache: cache.Options{
				DefaultNamespaces: map[string]cache.Config{
					ns.Name: {},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		// 3. Register the controller and its watches.
		reconciler := &ForgejoRunnerReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}

		Expect(reconciler.SetupWithManager(mgr)).
			To(Succeed())

		// 4. Start the Manager in a goroutine.
		managerCtx, cancelManager := context.WithCancel(ctx)
		managerDone := make(chan error, 1)

		go func() {
			managerDone <- mgr.Start(managerCtx)
		}()

		// Stop the Manager before envtest shuts down.
		DeferCleanup(func() {
			cancelManager()

			select {
			case err := <-managerDone:
				Expect(err).NotTo(HaveOccurred())
			case <-time.After(10 * time.Second):
				Fail("Manager did not stop")
			}
		})

		// Wait until the manager's informer cache is ready.
		syncCtx, cancelSync := context.WithTimeout(
			managerCtx,
			20*time.Second,
		)
		defer cancelSync()

		Expect(mgr.GetCache().WaitForCacheSync(syncCtx)).
			To(BeTrue())

		// 5. Create a ForgejoRunner without its Secret.
		key := types.NamespacedName{
			Name:      "event-test",
			Namespace: ns.Name,
		}

		runner := &civ1alpha1.ForgejoRunner{
			ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
			},
			Spec: civ1alpha1.ForgejoRunnerSpec{
				Image: "data.forgejo.org/forgejo/runner:13",
				ConfigSecretRef: corev1.LocalObjectReference{
					Name: "runner-config",
				},
			},
		}

		Expect(k8sClient.Create(ctx, runner)).
			To(Succeed())

		// Helper: assert the observed Ready condition.
		expectReady := func(
			status metav1.ConditionStatus,
			reason string,
			timeout time.Duration,
		) {
			Eventually(func(g Gomega) {
				current := &civ1alpha1.ForgejoRunner{}

				g.Expect(k8sClient.Get(
					ctx, key, current,
				)).To(Succeed())

				condition := meta.FindStatusCondition(
					current.Status.Conditions,
					"Ready",
				)

				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(status))
				g.Expect(condition.Reason).To(Equal(reason))
			}, timeout, 100*time.Millisecond).Should(Succeed())
		}

		By("detecting a missing Secret automatically")

		expectReady(
			metav1.ConditionFalse,
			"SecretNotFound",
			10*time.Second,
		)

		// 6. Create the missing Secret.
		By("reconciling after Secret creation")

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "runner-config",
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				"runner-config.yml": []byte("runner: {}\n"),
			},
		}

		Expect(k8sClient.Create(ctx, secret)).
			To(Succeed())

		// The Secret watch must trigger Deployment creation.
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(
				ctx, key, deployment,
			)).To(Succeed())

			g.Expect(deployment.Spec.Replicas).
				NotTo(BeNil())
			g.Expect(*deployment.Spec.Replicas).
				To(Equal(int32(1)))
		}, 8*time.Second, 100*time.Millisecond).
			Should(Succeed())

		// 7. Simulate Deployment readiness.
		// envtest has no Deployment controller.
		By("reacting to Deployment status updates")

		deployment := &appsv1.Deployment{}
		Expect(k8sClient.Get(
			ctx, key, deployment,
		)).To(Succeed())

		deployment.Status.Replicas = 1
		deployment.Status.ReadyReplicas = 1
		deployment.Status.AvailableReplicas = 1
		deployment.Status.UpdatedReplicas = 1
		deployment.Status.ObservedGeneration =
			deployment.Generation

		Expect(k8sClient.Status().Update(
			ctx, deployment,
		)).To(Succeed())

		// The Deployment watch must update runner status.
		expectReady(
			metav1.ConditionTrue,
			"DeploymentAvailable",
			8*time.Second,
		)

		// 8. Delete the Secret.
		By("detecting Secret deletion automatically")

		Expect(k8sClient.Delete(ctx, secret)).
			To(Succeed())

		expectReady(
			metav1.ConditionFalse,
			"SecretNotFound",
			8*time.Second,
		)

		// 9. Restore the Secret.
		By("recovering when the Secret returns")

		restoredSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "runner-config",
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				"runner-config.yml": []byte("runner: {}\n"),
			},
		}

		Expect(k8sClient.Create(ctx, restoredSecret)).
			To(Succeed())

		expectReady(
			metav1.ConditionTrue,
			"DeploymentAvailable",
			8*time.Second,
		)

		// 10. Simulate Deployment configuration drift.
		By("correcting Deployment drift automatically")

		Expect(k8sClient.Get(
			ctx, key, deployment,
		)).To(Succeed())

		deployment.Spec.Template.Spec.
			Containers[0].Image = "nginx:latest"

		Expect(k8sClient.Update(
			ctx, deployment,
		)).To(Succeed())

		// No direct call to Reconcile().
		// The Deployment watch must trigger reconciliation.
		Eventually(func(g Gomega) {
			current := &appsv1.Deployment{}

			g.Expect(k8sClient.Get(
				ctx, key, current,
			)).To(Succeed())

			g.Expect(
				current.Spec.Template.Spec.Containers[0].Image,
			).To(Equal(runner.Spec.Image))
		}, 8*time.Second, 100*time.Millisecond).
			Should(Succeed())
	})
})
