{ ... }:
{
  # Every daemon exports directly through its management NAT to one suite collector.
  systemd.services.nixmox.environment = {
    OTEL_EXPORTER_OTLP_ENDPOINT = "http://10.0.2.2:14318";
    OTEL_METRIC_EXPORT_INTERVAL = "5000";
    OTEL_RESOURCE_ATTRIBUTES = "service.namespace=nixmox-e2e";
  };
}
