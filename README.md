# example-operator

- This operator reconciles an Example CR and creates Deployments with specific replica count and image, followed by creation of Service and optionally, an Ingress too.
- The code is instrumented to enable sending trace data via OpenTelemetry, which then gets consumed by the OpenTelemetry Collector and passed on to the Tempo service.
- Tempo is great at visualising the traces giving us an excellent way to see what's going on in our service.
