package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
	"go.opentelemetry.io/otel/trace"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appv1 "github.com/dheerajodha/openshift-operator-otel-tempo-demo/api/v1"
)

type ExampleReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
	Tracer trace.Tracer
}

// initializeTracer sets up the OpenTelemetry tracer with a specified Tempo endpoint.
func initializeTracer() trace.Tracer {
	ctx := context.Background()
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint("opentelemetry-collector.opentelemetry.svc:4318"),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		panic("Failed to create OTLP exporter: " + err.Error())
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String("example-operator"),
		),
	)
	if err != nil {
		panic("Failed to create resource: " + err.Error())
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return otel.Tracer("example-operator")
}

// Reconcile handles Example resources.
func (r *ExampleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tracer := r.Tracer
	ctx, span := tracer.Start(ctx, "Reconcile Example Resource",
		trace.WithAttributes(attribute.String("example.name", req.Name),
			attribute.String("example.namespace", req.Namespace)))
	defer span.End()

	log := log.FromContext(ctx)

	// Step 1: Fetch the Example instance
	example, err := r.fetchExampleInstance(ctx, req)
	if err != nil || example == nil {
		return ctrl.Result{}, err
	}

	// Step 2: Create or update the Deployment
	if err := r.reconcileDeployment(ctx, example, log); err != nil {
		return ctrl.Result{}, err
	}

	// Step 3: Create or update the Service
	if err := r.reconcileService(ctx, example, log); err != nil {
		return ctrl.Result{}, err
	}

	// Step 4: Optionally create or update the Ingress
	if example.Spec.EnableIngress {
		if err := r.reconcileIngress(ctx, example, log); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 5: Update status
	if err := r.updateStatus(ctx, example); err != nil {
		return ctrl.Result{}, err
	}

	span.AddEvent("Reconcile completed successfully")
	return ctrl.Result{}, nil
}

func (r *ExampleReconciler) fetchExampleInstance(ctx context.Context, req ctrl.Request) (*appv1.Example, error) {
	_, span := r.Tracer.Start(ctx, "Fetch Example Instance")
	defer span.End()

	example := &appv1.Example{}
	if err := r.Get(ctx, req.NamespacedName, example); err != nil {
		if errors.IsNotFound(err) {
			span.AddEvent("Example resource not found")
			return nil, nil
		}
		span.RecordError(err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("example.spec.replicas", example.Spec.Replicas),
		attribute.String("example.spec.image", example.Spec.Image))
	return example, nil
}

func (r *ExampleReconciler) reconcileDeployment(ctx context.Context, example *appv1.Example, log logr.Logger) error {
	_, span := r.Tracer.Start(ctx, "Reconcile Deployment")
	defer span.End()

	replicas := int32(example.Spec.Replicas)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-" + example.Name,
			Namespace: example.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": example.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": example.Name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "example",
						Image: example.Spec.Image,
					}},
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(example, deployment, r.Scheme); err != nil {
		span.RecordError(err)
		return err
	}

	found := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, found); err != nil {
		if errors.IsNotFound(err) {
			span.AddEvent("Creating Deployment")
			if err := r.Create(ctx, deployment); err != nil {
				span.RecordError(err)
				return err
			}
			log.Info("Created Deployment", "name", deployment.Name)
		} else {
			span.RecordError(err)
			return err
		}
	} else if *found.Spec.Replicas != replicas || found.Spec.Template.Spec.Containers[0].Image != example.Spec.Image {
		span.AddEvent("Updating Deployment")
		found.Spec = deployment.Spec
		if err := r.Update(ctx, found); err != nil {
			span.RecordError(err)
			return err
		}
		log.Info("Updated Deployment", "name", deployment.Name)
	}
	return nil
}

func (r *ExampleReconciler) reconcileService(ctx context.Context, example *appv1.Example, log logr.Logger) error {
	_, span := r.Tracer.Start(ctx, "Reconcile Service")
	defer span.End()

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-" + example.Name + "-service",
			Namespace: example.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": example.Name},
			Ports: []corev1.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	foundService := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, foundService); err != nil {
		if errors.IsNotFound(err) {
			span.AddEvent("Creating Service")
			if err := r.Create(ctx, service); err != nil {
				span.RecordError(err)
				return err
			}
			log.Info("Created Service", "name", service.Name)
		} else {
			span.RecordError(err)
			return err
		}
	} else if !equalServices(service, foundService) {
		foundService.Spec = service.Spec
		if err := r.Update(ctx, foundService); err != nil {
			span.RecordError(err)
			return err
		}
		log.Info("Updated Service", "name", service.Name)
	}
	return nil
}

func (r *ExampleReconciler) reconcileIngress(ctx context.Context, example *appv1.Example, log logr.Logger) error {
	_, span := r.Tracer.Start(ctx, "Reconcile Ingress")
	defer span.End()

	pathTypePrefix := networkingv1.PathTypePrefix
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-" + example.Name + "-ingress",
			Namespace: example.Namespace,
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: example.Spec.Host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathTypePrefix,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: "test-" + example.Name + "-service",
									Port: networkingv1.ServiceBackendPort{Number: 80},
								},
							},
						}},
					},
				},
			}},
		},
	}

	foundIngress := &networkingv1.Ingress{}
	if err := r.Get(ctx, types.NamespacedName{Name: ingress.Name, Namespace: ingress.Namespace}, foundIngress); err != nil {
		if errors.IsNotFound(err) {
			span.AddEvent("Creating Ingress")
			if err := r.Create(ctx, ingress); err != nil {
				span.RecordError(err)
				return err
			}
			log.Info("Created Ingress", "name", ingress.Name)
		} else {
			span.RecordError(err)
			return err
		}
	} else if !equalIngresses(ingress.Spec, foundIngress.Spec) {
		foundIngress.Spec = ingress.Spec
		if err := r.Update(ctx, foundIngress); err != nil {
			span.RecordError(err)
			return err
		}
		log.Info("Updated Ingress", "name", ingress.Name)
	}
	return nil
}

func (r *ExampleReconciler) updateStatus(ctx context.Context, example *appv1.Example) error {
	_, span := r.Tracer.Start(ctx, "Update Status")
	defer span.End()

	found := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: "test-" + example.Name, Namespace: example.Namespace}, found); err != nil {
		span.RecordError(err)
		return err
	}

	if found.Status.AvailableReplicas != example.Status.AvailableReplicas {
		example.Status.AvailableReplicas = found.Status.AvailableReplicas
		span.AddEvent("Updating status", trace.WithAttributes(attribute.Int("availableReplicas", int(found.Status.AvailableReplicas))))
		if err := r.Status().Update(ctx, example); err != nil {
			span.RecordError(err)
			return err
		}
	}
	return nil
}

// Helper functions to compare specs for equality
func equalServices(spec1, spec2 *corev1.Service) bool {
	return spec1.Name == spec2.Name
}

func equalIngresses(spec1, spec2 networkingv1.IngressSpec) bool {
	return spec1.Rules[0].Host == spec2.Rules[0].Host
}

func (r *ExampleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appv1.Example{}).
		Complete(r)
}

func NewExampleReconciler(client client.Client, scheme *runtime.Scheme) *ExampleReconciler {
	tracer := initializeTracer()
	return &ExampleReconciler{
		Client: client,
		Scheme: scheme,
		Tracer: tracer,
	}
}
