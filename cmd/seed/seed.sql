-- seed.sql: idempotent fixture data for the Incident Investigator Agent.
-- Each table is cleared first, then re-populated, so re-running produces the
-- same state without duplicate-key errors. The scenario is a correlated P1:
-- a checkout deployment 15 minutes before an incident, matching KB guidance
-- and a degraded checkout health signal.

DELETE FROM incidents;
DELETE FROM deployments;
DELETE FROM knowledge_base;
DELETE FROM service_health;

INSERT INTO incidents (incident_id, title, severity, affected_services, created_at)
VALUES ('INC-1001', 'Checkout returning 5xx errors', 'P1', '["checkout","payments"]', TIMESTAMPTZ '2024-01-15 10:00:00+00');

INSERT INTO deployments (deployment_id, service_name, version, deployed_at, deployed_by)
VALUES ('dep-1', 'checkout', 'v2.3.1', TIMESTAMPTZ '2024-01-15 09:45:00+00', 'alice');

INSERT INTO deployments (deployment_id, service_name, version, deployed_at, deployed_by)
VALUES ('dep-2', 'payments', 'v1.0.5', TIMESTAMPTZ '2024-01-15 08:30:00+00', 'bob');

INSERT INTO deployments (deployment_id, service_name, version, deployed_at, deployed_by)
VALUES ('dep-3', 'search', 'v4.2.0', TIMESTAMPTZ '2024-01-10 00:00:00+00', 'carol');

INSERT INTO knowledge_base (article_id, title, article_type, service_name, symptom_tags, impact_summary, body, severity_scope, last_updated)
VALUES ('kb-1', 'Checkout 5xx troubleshooting', 'TSG', 'checkout', '["5xx_errors","high_latency","db_pool_exhaustion"]', 'Checkout 5xx spikes are typically caused by database connection pool exhaustion following a deployment. Roll back the most recent checkout deployment and verify pool metrics.', '1. Check DB pool saturation. 2. Correlate with recent deploys. 3. Roll back if a deploy precedes the spike. 4. Scale pool if organic.', 'P1,P2', TIMESTAMPTZ '2024-01-12 00:00:00+00');

INSERT INTO knowledge_base (article_id, title, article_type, service_name, symptom_tags, impact_summary, body, severity_scope, last_updated)
VALUES ('kb-2', 'Payments service SOP', 'SOP', 'payments', '["timeout","5xx_errors"]', 'Payments timeouts can cascade from checkout failures. Verify upstream checkout health before paging the payments team.', '1. Verify checkout health. 2. Check payment gateway status. 3. Page payments on-call if isolated.', 'P1,P2,P3', TIMESTAMPTZ '2024-01-11 00:00:00+00');

INSERT INTO service_health (service_name, health_status, last_checked, details)
VALUES ('checkout', 'degraded', TIMESTAMPTZ '2024-01-15 10:02:00+00', 'DB connection pool at 98% utilisation');

INSERT INTO service_health (service_name, health_status, last_checked, details)
VALUES ('payments', 'healthy', TIMESTAMPTZ '2024-01-15 10:02:00+00', 'nominal');
