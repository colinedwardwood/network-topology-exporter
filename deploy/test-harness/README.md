# Network Topology Exporter Test Harness

Turnkey stack to scrape metrics and logs from the exporter and ship them to Grafana Cloud.

## Quickstart

1. **Build the exporter image:**
   ```bash
   docker build -t network-topology-exporter:latest .
   ```

2. **Configure the exporter:**
   ```bash
   cp deploy/test-harness/config.yaml.example deploy/test-harness/config.yaml
   # Edit config.yaml to set your discovery.scope.cidr_allow_list
   ```

3. **Configure Alloy (Grafana Cloud):**
   ```bash
   cp deploy/test-harness/alloy/config.alloy.example deploy/test-harness/alloy/config.alloy
   # Two placeholders to fill in (URLs + user IDs are pre-filled for the
   # shared networko11ydev.grafana.net stack):
   #   YOUR_GLC_TOKEN   — write token from the stack's connections page
   #   YOUR_TESTER_ID   — your unique kebab-case slug
   # Full instructions: see the "00. Getting Started" dashboard.
   ```

4. **Launch the stack:**
   ```bash
   cd deploy/test-harness
   export SNMP_COMMUNITY=your_community
   docker compose up -d
   ```

5. **Verify:**
   - Exporter metrics: `curl http://localhost:9100/metrics`
   - Alloy UI (optional): `http://localhost:12345` (if port mapped)
   - Check your Grafana Cloud instance for the `network_topology_*` metrics.

## What's included

- **topology-exporter**: The discovery engine.
- **Alloy**: Scrapes `/metrics`, tails container logs via the Docker socket, and ships to Grafana Cloud with your `tester_id` label.

## Troubleshooting

- **No logs in Loki:** Ensure `/var/run/docker.sock` is accessible to the Alloy container.
- **No metrics in Mimir:** Check Alloy logs (`docker logs test-harness-alloy-1`) for authentication errors.
- **Exporter not finding devices:** Verify `config.yaml` has the correct `cidr_allow_list` and `SNMP_COMMUNITY` is set.
