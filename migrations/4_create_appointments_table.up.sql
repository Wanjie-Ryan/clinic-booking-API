CREATE TABLE IF NOT EXISTS appointments (
    -- IDs are usinged cause they can't be -ve, unsigned supports +ve numbers upwards.
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    doctor_id           BIGINT UNSIGNED NOT NULL,
    patient_id          BIGINT UNSIGNED NOT NULL,
    -- both are stored in UTC 
    start_time          DATETIME NOT NULL,
    end_time            DATETIME NOT NULL,
    status              ENUM('booked', 'cancelled') NOT NULL DEFAULT 'booked',
    cancellation_reason VARCHAR(500) DEFAULT NULL,

    -- The double-booking guard. NULL whenever status != 'booked', so InnoDB's
    -- unique index treats every cancelled row as distinct from every other row
    -- (NULLs never collide). Two 'booked' rows for the same doctor at the same
    -- start_time produce the same key string and the second INSERT fails with
    -- MySQL error 1062, which the application maps to HTTP 409.
    active_slot_key VARCHAR(64) GENERATED ALWAYS AS (
        CASE WHEN status = 'booked'
             THEN CONCAT(doctor_id, '-', start_time)
             ELSE NULL
        END
    ) STORED,

    created  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,

    PRIMARY KEY (id),
    UNIQUE KEY uq_active_slot (active_slot_key),
    KEY idx_doctor_start (doctor_id, start_time),
    KEY idx_patient_start (patient_id, start_time),
    CONSTRAINT chk_appointment_range CHECK (end_time > start_time),
    CONSTRAINT fk_appointments_doctor FOREIGN KEY (doctor_id)
        REFERENCES doctors (id),
    CONSTRAINT fk_appointments_patient FOREIGN KEY (patient_id)
        REFERENCES patients (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
