CREATE TABLE outbound (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id      INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    message_ref     TEXT NOT NULL DEFAULT '',
    task_id         TEXT NOT NULL DEFAULT '',
    from_address    TEXT NOT NULL DEFAULT '',
    recipients_json TEXT NOT NULL DEFAULT '[]',
    subject         TEXT NOT NULL DEFAULT '',
    size            INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'accepted',
    attempt         INTEGER NOT NULL DEFAULT 0,
    smtp_code       INTEGER NOT NULL DEFAULT 0,
    enhanced_status TEXT NOT NULL DEFAULT '',
    remote_response TEXT NOT NULL DEFAULT '',
    reason          TEXT NOT NULL DEFAULT '',
    accepted_at     INTEGER NOT NULL DEFAULT 0,
    occurred_at     INTEGER NOT NULL DEFAULT 0,
    next_retry_at   INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX outbound_message_ref_idx ON outbound(message_ref) WHERE message_ref <> '';
CREATE INDEX outbound_task_idx ON outbound(task_id) WHERE task_id <> '';
CREATE INDEX outbound_account_idx ON outbound(account_id, created_at DESC);
