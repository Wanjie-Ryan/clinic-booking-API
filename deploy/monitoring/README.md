# VPS monitoring stack (vps branch only)

Self-hosted Prometheus + Grafana, alongside the Uptrace pipeline (which is unchanged).

    cd deploy/monitoring
    cp .env.example .env      # set GRAFANA_ADMIN_PASSWORD / GRAFANA_ROOT_URL
    docker compose up -d

The app stack must already be running: this stack joins its `clinic-booking_clinicnet` network
and scrapes `clinic-app:8080/metrics`.

## Nginx

Do NOT expose `/metrics` publicly. In the app's server block (`/etc/nginx/sites-enabled/clinic-booking`):

    location = /metrics { return 404; }

Prometheus scrapes over the docker network, so this does not affect it.

Grafana gets its own subdomain (same pattern as Infisical), proxying to `127.0.0.1:3000`
(websocket upgrade headers are needed for Grafana Live), then `certbot --nginx -d <subdomain>`.
Prometheus is intentionally not exposed: it has no auth.

## Dashboard

`grafana/dashboards/go-runtime.json` is Grafana.com dashboard 14061 with the three hard-coded
`job=~"tns_app"` matchers rewritten to `$job`. To refresh it:

    curl -sSL https://grafana.com/api/dashboards/14061/revisions/latest/download \
      | sed 's/tns_app/$job/g' > grafana/dashboards/go-runtime.json
