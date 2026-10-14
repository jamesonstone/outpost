-- Synthetic integration fixture only. Runtime never installs tables or indexes.
CREATE TABLE outpost_test_events (
 id text PRIMARY KEY,
 environment text,
 tenant text NOT NULL,
 order_id text NOT NULL,
 partner text,
 placed_at timestamptz NOT NULL,
 link text,
 kind text,
 traffic text,
 origin text,
 tenant_class text
);
CREATE INDEX outpost_test_events_scan ON outpost_test_events (placed_at, id COLLATE "C");
INSERT INTO outpost_test_events VALUES
 ('synthetic-a', 'production', 'synthetic-tenant', 'synthetic-order-a', 'synthetic-partner',
 '2030-01-01T00:00:00Z', 'https://example.test/orders/synthetic-order-a',
 'first_placement', 'production', 'partner_api', 'production');
-- Projection example (configure this SELECT separately):
-- SELECT id, environment, tenant, order_id, partner, placed_at, link, kind,
--        traffic, origin, tenant_class FROM outpost_test_events
