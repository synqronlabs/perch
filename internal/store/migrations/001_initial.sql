CREATE TABLE accounts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    primary_address TEXT NOT NULL COLLATE NOCASE UNIQUE,
    display_name    TEXT NOT NULL DEFAULT '',
    password_hash   TEXT NOT NULL,
    disabled        INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);

CREATE TABLE addresses (
    address    TEXT NOT NULL COLLATE NOCASE PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('primary','alias','catchall')),
    created_at INTEGER NOT NULL
);
CREATE INDEX addresses_account_idx ON addresses(account_id);

CREATE TABLE mailboxes (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name            TEXT NOT NULL COLLATE NOCASE,
    uid_validity    INTEGER NOT NULL,
    uid_next        INTEGER NOT NULL DEFAULT 1,
    highest_modseq  INTEGER NOT NULL DEFAULT 1,
    subscribed      INTEGER NOT NULL DEFAULT 1,
    special_use     TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    UNIQUE(account_id, name)
);
CREATE INDEX mailboxes_account_idx ON mailboxes(account_id);

CREATE TABLE blobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    sha256      TEXT NOT NULL UNIQUE,
    size        INTEGER NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('local','s3')),
    ref         TEXT NOT NULL DEFAULT '',
    refcount    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

CREATE TABLE blob_data (
    blob_id  INTEGER PRIMARY KEY REFERENCES blobs(id) ON DELETE CASCADE,
    encoding TEXT NOT NULL,
    data     BLOB NOT NULL
);

CREATE TABLE messages (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id        INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    mailbox_id        INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    uid               INTEGER NOT NULL,
    modseq            INTEGER NOT NULL,
    blob_id           INTEGER NOT NULL REFERENCES blobs(id),
    size              INTEGER NOT NULL,
    internal_date     INTEGER NOT NULL,
    message_id        TEXT NOT NULL DEFAULT '',
    subject           TEXT NOT NULL DEFAULT '',
    from_json         TEXT NOT NULL DEFAULT '[]',
    to_json           TEXT NOT NULL DEFAULT '[]',
    cc_json           TEXT NOT NULL DEFAULT '[]',
    bcc_json          TEXT NOT NULL DEFAULT '[]',
    reply_to_json     TEXT NOT NULL DEFAULT '[]',
    in_reply_to_json  TEXT NOT NULL DEFAULT '[]',
    references_json   TEXT NOT NULL DEFAULT '[]',
    sent_at           INTEGER,
    headers_json      TEXT NOT NULL DEFAULT '[]',
    mime_json         TEXT NOT NULL DEFAULT '',
    raw_sha256        TEXT NOT NULL DEFAULT '',
    s3_key            TEXT NOT NULL DEFAULT '',
    mxr_task_id       TEXT,
    mxr_correlation_id TEXT,
    is_spam           INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    UNIQUE(mailbox_id, uid)
);
CREATE INDEX messages_mailbox_uid_idx ON messages(mailbox_id, uid);
CREATE INDEX messages_mailbox_modseq_idx ON messages(mailbox_id, modseq);
CREATE UNIQUE INDEX messages_task_idx ON messages(mxr_task_id)
    WHERE mxr_task_id IS NOT NULL AND mxr_task_id <> '';
CREATE INDEX messages_account_sha_idx ON messages(account_id, raw_sha256)
    WHERE raw_sha256 <> '';

CREATE TABLE message_flags (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    flags      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE message_keywords (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    keyword    TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (message_id, keyword)
);

CREATE TABLE search_content (
    message_id  INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    subject     TEXT NOT NULL DEFAULT '',
    sender      TEXT NOT NULL DEFAULT '',
    recipients  TEXT NOT NULL DEFAULT '',
    headers     TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL DEFAULT '',
    attachments TEXT NOT NULL DEFAULT ''
);

CREATE VIRTUAL TABLE search_index USING fts5(
    subject, sender, recipients, headers, body, attachments,
    content='search_content',
    content_rowid='message_id',
    tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER search_ai AFTER INSERT ON search_content BEGIN
    INSERT INTO search_index(rowid, subject, sender, recipients, headers, body, attachments)
    VALUES (new.message_id, new.subject, new.sender, new.recipients, new.headers, new.body, new.attachments);
END;

CREATE TRIGGER search_ad AFTER DELETE ON search_content BEGIN
    INSERT INTO search_index(search_index, rowid, subject, sender, recipients, headers, body, attachments)
    VALUES ('delete', old.message_id, old.subject, old.sender, old.recipients, old.headers, old.body, old.attachments);
END;

CREATE TRIGGER search_au AFTER UPDATE ON search_content BEGIN
    INSERT INTO search_index(search_index, rowid, subject, sender, recipients, headers, body, attachments)
    VALUES ('delete', old.message_id, old.subject, old.sender, old.recipients, old.headers, old.body, old.attachments);
    INSERT INTO search_index(rowid, subject, sender, recipients, headers, body, attachments)
    VALUES (new.message_id, new.subject, new.sender, new.recipients, new.headers, new.body, new.attachments);
END;

CREATE TABLE dead_letter (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    address    TEXT NOT NULL DEFAULT '',
    task_id    TEXT NOT NULL DEFAULT '',
    reason     TEXT NOT NULL,
    raw        BLOB,
    created_at INTEGER NOT NULL
);

CREATE TABLE sync_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
