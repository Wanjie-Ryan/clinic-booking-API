-- A handful of test patients so the deployed API is bookable end-to-end without
-- direct database access -- there is no POST /patients endpoint (the scenario
-- describes patients booking against existing clinic records, not
-- self-registration; see README D-19).
INSERT INTO patients (id, name, email, phone) VALUES
    (1, 'Grace Achieng', 'grace.achieng@example.com', '+254700111111'),
    (2, 'Kevin Mutua', 'kevin.mutua@example.com', '+254700222222'),
    (3, 'Sarah Kimani', 'sarah.kimani@example.com', '+254700333333');
