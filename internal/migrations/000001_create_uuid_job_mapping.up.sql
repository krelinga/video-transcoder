CREATE TABLE uuid_job_mapping (
    uuid UUID PRIMARY KEY,
    river_job_id BIGINT NOT NULL REFERENCES river_job(id) ON DELETE CASCADE
);
