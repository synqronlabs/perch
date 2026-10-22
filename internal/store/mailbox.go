package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const mailboxColumns = `id, account_id, name, uid_validity, uid_next, highest_modseq, subscribed, special_use, created_at`

// ListMailboxes returns all mailboxes for an account ordered by name.
func (s *Store) ListMailboxes(ctx context.Context, accountID int64) ([]Mailbox, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT `+mailboxColumns+`
		FROM mailboxes WHERE account_id = ? ORDER BY name`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Mailbox
	for rows.Next() {
		m, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMailbox(row rowScanner) (Mailbox, error) {
	var m Mailbox
	var created int64
	if err := row.Scan(&m.ID, &m.AccountID, &m.Name, &m.UIDValidity, &m.UIDNext, &m.HighestModSeq, &m.Subscribed, &m.SpecialUse, &created); err != nil {
		return Mailbox{}, fmt.Errorf("scan mailbox: %w", err)
	}
	m.CreatedAt = time.Unix(created, 0).UTC()
	return m, nil
}

// MailboxByName returns one mailbox. INBOX is matched case-insensitively.
func (s *Store) MailboxByName(ctx context.Context, accountID int64, name string) (Mailbox, error) {
	name = canonicalMailboxName(name)
	m, err := scanMailbox(s.reader.QueryRowContext(ctx, `
		SELECT `+mailboxColumns+`
		FROM mailboxes WHERE account_id = ? AND name = ? COLLATE NOCASE`, accountID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Mailbox{}, ErrMailboxNotFound
	}
	return m, err
}

// MailboxByID returns one mailbox by database identifier.
func (s *Store) MailboxByID(ctx context.Context, id int64) (Mailbox, error) {
	m, err := scanMailbox(s.reader.QueryRowContext(ctx, `
		SELECT `+mailboxColumns+`
		FROM mailboxes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Mailbox{}, ErrMailboxNotFound
	}
	return m, err
}

// CreateMailbox creates a mailbox and any missing parent mailboxes.
func (s *Store) CreateMailbox(ctx context.Context, accountID int64, name, specialUse string) (Mailbox, error) {
	name = canonicalMailboxName(name)
	if err := validateMailboxName(name); err != nil {
		return Mailbox{}, err
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return Mailbox{}, fmt.Errorf("begin create mailbox: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailboxes WHERE account_id = ? AND name = ? COLLATE NOCASE`, accountID, name).Scan(&existing); err != nil {
		return Mailbox{}, fmt.Errorf("check mailbox: %w", err)
	}
	if existing > 0 {
		return Mailbox{}, fmt.Errorf("%w: mailbox %s", ErrDuplicate, name)
	}
	if err := ensureParentMailboxes(ctx, tx, accountID, name, now); err != nil {
		return Mailbox{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO mailboxes
			(account_id, name, uid_validity, uid_next, highest_modseq, subscribed, special_use, created_at)
		VALUES (?, ?, ?, 1, 1, 1, ?, ?)`, accountID, name, randomUIDValidity(), specialUse, now); err != nil {
		return Mailbox{}, fmt.Errorf("create mailbox %q: %w", name, err)
	}
	m, err := scanMailbox(tx.QueryRowContext(ctx, `
		SELECT `+mailboxColumns+`
		FROM mailboxes WHERE account_id = ? AND name = ? COLLATE NOCASE`, accountID, name))
	if err != nil {
		return Mailbox{}, fmt.Errorf("read created mailbox: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Mailbox{}, fmt.Errorf("commit create mailbox: %w", err)
	}
	return m, nil
}

// DeleteMailbox deletes an empty-of-children mailbox and all its messages. It
// refuses to delete INBOX.
func (s *Store) DeleteMailbox(ctx context.Context, accountID int64, name string) error {
	name = canonicalMailboxName(name)
	if strings.EqualFold(name, "INBOX") {
		return errors.New("store: INBOX cannot be deleted")
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete mailbox: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	id, err := mailboxIDByName(ctx, tx, accountID, name)
	if err != nil {
		return err
	}
	var children int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM mailboxes WHERE account_id = ? AND name COLLATE NOCASE LIKE ? ESCAPE '\'`,
		accountID, escapeLike(name)+"/%").Scan(&children); err != nil {
		return fmt.Errorf("count child mailboxes: %w", err)
	}
	if children > 0 {
		return ErrMailboxHasChildren
	}
	if err := deleteMessagesInMailbox(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mailboxes WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete mailbox: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete mailbox: %w", err)
	}
	return nil
}

// RenameMailbox renames a mailbox and its descendants. INBOX cannot be
// renamed; clients should create a new mailbox and move messages instead.
func (s *Store) RenameMailbox(ctx context.Context, accountID int64, oldName, newName string) error {
	oldName = canonicalMailboxName(oldName)
	newName = canonicalMailboxName(newName)
	if strings.EqualFold(oldName, "INBOX") {
		return errors.New("store: INBOX cannot be renamed")
	}
	if err := validateMailboxName(newName); err != nil {
		return err
	}
	if strings.EqualFold(oldName, newName) {
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rename mailbox: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	id, err := mailboxIDByName(ctx, tx, accountID, oldName)
	if err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailboxes WHERE account_id = ? AND name = ? COLLATE NOCASE`, accountID, newName).Scan(&exists); err != nil {
		return fmt.Errorf("check destination: %w", err)
	}
	if exists > 0 {
		return fmt.Errorf("%w: mailbox %s", ErrDuplicate, newName)
	}
	now := time.Now().UTC().Unix()
	if err := ensureParentMailboxes(ctx, tx, accountID, newName, now); err != nil {
		return err
	}
	// Rename descendants first so a destination that is a prefix of the source
	// cannot collide with the source tree.
	if _, err := tx.ExecContext(ctx, `
		UPDATE mailboxes SET name = ? || substr(name, length(?) + 1)
		WHERE account_id = ? AND name COLLATE NOCASE LIKE ? ESCAPE '\' AND name <> ? COLLATE NOCASE`,
		newName, oldName, accountID, escapeLike(oldName)+"/%", oldName); err != nil {
		return fmt.Errorf("rename child mailboxes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mailboxes SET name = ? WHERE id = ?`, newName, id); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: mailbox %s", ErrDuplicate, newName)
		}
		return fmt.Errorf("rename mailbox: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rename mailbox: %w", err)
	}
	return nil
}

// SetSubscribed changes a mailbox's subscription state.
func (s *Store) SetSubscribed(ctx context.Context, accountID int64, name string, subscribed bool) error {
	name = canonicalMailboxName(name)
	res, err := s.writer.ExecContext(ctx, `UPDATE mailboxes SET subscribed = ? WHERE account_id = ? AND name = ? COLLATE NOCASE`,
		subscribed, accountID, name)
	if err != nil {
		return fmt.Errorf("set subscribed: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrMailboxNotFound
	}
	return nil
}

// MailboxStatus summarises a mailbox for SELECT and STATUS.
type MailboxStatus struct {
	Mailbox     Mailbox
	Messages    uint32
	Unseen      uint32
	Deleted     uint32
	Size        int64
	FirstUnseen uint32
}

// Status returns counts and size for a mailbox.
func (s *Store) Status(ctx context.Context, mailboxID int64) (MailboxStatus, error) {
	m, err := s.MailboxByID(ctx, mailboxID)
	if err != nil {
		return MailboxStatus{}, err
	}
	var st MailboxStatus
	st.Mailbox = m
	var unseen, deleted int64
	err = s.reader.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(m.size),0),
		       COALESCE(SUM(CASE WHEN (COALESCE(f.flags,0) & ?) = 0 THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN (COALESCE(f.flags,0) & ?) <> 0 THEN 1 ELSE 0 END),0)
		FROM messages m
		LEFT JOIN message_flags f ON f.message_id = m.id
		WHERE m.mailbox_id = ?`, uint32(FlagSeen), uint32(FlagDeleted), mailboxID).
		Scan(&st.Messages, &st.Size, &unseen, &deleted)
	if err != nil {
		return MailboxStatus{}, fmt.Errorf("mailbox status: %w", err)
	}
	st.Unseen = uint32(unseen)
	st.Deleted = uint32(deleted)
	if st.Unseen > 0 {
		if err := s.reader.QueryRowContext(ctx, `
			SELECT m.uid FROM messages m
			LEFT JOIN message_flags f ON f.message_id = m.id
			WHERE m.mailbox_id = ? AND (COALESCE(f.flags,0) & ?) = 0
			ORDER BY m.uid LIMIT 1`, mailboxID, uint32(FlagSeen)).Scan(&st.FirstUnseen); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return MailboxStatus{}, fmt.Errorf("first unseen: %w", err)
		}
	}
	return st, nil
}

func mailboxIDByName(ctx context.Context, tx *sql.Tx, accountID int64, name string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM mailboxes WHERE account_id = ? AND name = ? COLLATE NOCASE`, accountID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrMailboxNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lookup mailbox: %w", err)
	}
	return id, nil
}

func ensureParentMailboxes(ctx context.Context, tx *sql.Tx, accountID int64, name string, now int64) error {
	parts := strings.Split(name, "/")
	for i := 0; i < len(parts)-1; i++ {
		parent := strings.Join(parts[:i+1], "/")
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO mailboxes
				(account_id, name, uid_validity, uid_next, highest_modseq, subscribed, special_use, created_at)
			VALUES (?, ?, ?, 1, 1, 1, '', ?)`, accountID, parent, randomUIDValidity(), now); err != nil {
			return fmt.Errorf("create parent mailbox %q: %w", parent, err)
		}
	}
	return nil
}

func deleteMessagesInMailbox(ctx context.Context, tx *sql.Tx, mailboxID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, blob_id FROM messages WHERE mailbox_id = ?`, mailboxID)
	if err != nil {
		return fmt.Errorf("list mailbox messages: %w", err)
	}
	var ids, blobs []int64
	for rows.Next() {
		var id, blobID int64
		if err := rows.Scan(&id, &blobID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan mailbox message: %w", err)
		}
		ids = append(ids, id)
		blobs = append(blobs, blobID)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close mailbox messages: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete message: %w", err)
		}
	}
	for _, blobID := range blobs {
		if err := releaseBlob(ctx, tx, blobID); err != nil {
			return err
		}
	}
	return nil
}

func canonicalMailboxName(name string) string {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	if strings.HasPrefix(strings.ToUpper(name), "INBOX/") {
		return "INBOX/" + name[len("INBOX/"):]
	}
	return name
}

func validateMailboxName(name string) error {
	if name == "" {
		return errors.New("store: mailbox name is empty")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return errors.New("store: mailbox name has an empty component")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '\\' || r == '*' || r == '%' {
			return fmt.Errorf("store: mailbox name contains invalid character %q", r)
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("store: mailbox name has an empty or reserved component")
		}
	}
	return nil
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
