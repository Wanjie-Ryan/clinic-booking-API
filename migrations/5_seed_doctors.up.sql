INSERT INTO doctors (id, name) VALUES
    (1, 'Dr. Amina Yusuf'),
    (2, 'Dr. Brian Otieno'),
    (3, 'Dr. Carol Njoroge'),
    (4, 'Dr. David Mwangi'),
    (5, 'Dr. Faith Wanjiru');

-- Monday(1) to Friday(5), 08:00-13:00 and 14:00-17:00, with a lunch break
-- represented by the gap between the two ranges (README D-03).
INSERT INTO doctor_working_hours (doctor_id, day_of_week, start_time, end_time)
SELECT d.id, wd.day_of_week, r.start_time, r.end_time
FROM doctors d
CROSS JOIN (SELECT 1 AS day_of_week UNION SELECT 2 UNION SELECT 3 UNION SELECT 4 UNION SELECT 5) wd
CROSS JOIN (
    SELECT '08:00:00' AS start_time, '13:00:00' AS end_time
    UNION ALL
    SELECT '14:00:00', '17:00:00'
) r;
-- UNION stacks the results of separate SELECT statements into one list, one on top of the other. It doesn't need a real table to pull from.
-- CROSS JOIN matches rows based on some shared column, has no matching condition at all - it just pairs every row of one table with every row of another.
-- If table A has 5 rows and table B has 2, CROSS JOIN gives you 5 * 2 = 10 rows.