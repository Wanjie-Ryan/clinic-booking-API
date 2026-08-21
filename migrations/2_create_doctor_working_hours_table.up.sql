CREATE TABLE IF NOT EXISTS doctor_working_hours (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    doctor_id   BIGINT UNSIGNED NOT NULL,
    -- 0 = Sunday ... 6 = Saturday, matches Go's time.Weekday
    day_of_week TINYINT UNSIGNED NOT NULL,
    start_time  TIME NOT NULL,
    end_time    TIME NOT NULL,
    created     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    -- index key here
    KEY idx_doctor_day (doctor_id, day_of_week),
    -- a constraint is a rule that is enforced by the DB on every single write, in this case the constraint is chk_working_hours_day
    -- day_of_week is between 0 to 6, meaning MYSQL will reject any insert/update where days_of_week is 7 and over.
    CONSTRAINT chk_working_hours_day CHECK (day_of_week BETWEEN 0 AND 6),
    CONSTRAINT chk_working_hours_range CHECK (end_time > start_time),
    -- FOREIGN_KEY - rule that references sth that actually exists.
    -- MYSQL will refuse to insert a working hours row pointing at a doctor_id 99 if no doctor with that id exists.
    CONSTRAINT fk_working_hours_doctor FOREIGN KEY (doctor_id)
        REFERENCES doctors (id) ON DELETE CASCADE
        -- when doctor is deleted (parent), delete also the doctors working hours here
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
