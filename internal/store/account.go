package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// Common store errors.
var (
	ErrNotFound           = errors.New("store: not found")
	ErrDuplicate          = errors.New("store: already exists")
	ErrNoRecipient        = errors.New("store: recipient does not map to an account")
	ErrMailboxNotFound    = errors.New("store: mailbox not found")
	ErrMailboxHasChildren = errors.New("store: mailbox has children")
	ErrAccountDisabled    = errors.New("store: account is disabled")
)

// Address is a decoded RFC 5322 mailbox.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Header is one raw header field.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Account is one local mailbox account.
type Account struct {
	ID             int64
	PrimaryAddress string
	DisplayName    string
	PasswordHash   string
	Disabled       bool
	CreatedAt      time.Time
}

// Mailbox is one IMAP mailbox belonging to an account.
type Mailbox struct {
	ID            int64
	AccountID     int64
	Name          string
	UIDValidity   uint32
	UIDNext       uint32
	HighestModSeq uint64
	Subscribed    bool
	SpecialUse    string
	CreatedAt     time.Time
}

// CreateAccount creates an account, its primary address, and the default
// mailbox set in one transaction. passwordHash must already be hashed.
func (s *Store) CreateAccount(ctx context.Context, address, displayName, passwordHash string) (Account, error) {
	address, err := NormalizeAddress(address)
	if err != nil {
		return Account{}, err
	}
	now := time.Now().UTC()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("begin create account: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO accounts (primary_address, display_name, password_hash, created_at)
		VALUES (?, ?, ?, ?)`, address, strings.TrimSpace(displayName), passwordHash, now.Unix())
	if err != nil {
		if isUnique(err) {
			return Account{}, fmt.Errorf("%w: account %s", ErrDuplicate, address)
		}
		return Account{}, fmt.Errorf("insert account: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Account{}, fmt.Errorf("account id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO addresses (address, account_id, kind, created_at) VALUES (?, ?, 'primary', ?)`,
		address, id, now.Unix()); err != nil {
		return Account{}, fmt.Errorf("insert primary address: %w", err)
	}
	for _, m := range defaultMailboxes() {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mailboxes (account_id, name, uid_validity, uid_next, highest_modseq, subscribed, special_use, created_at)
			VALUES (?, ?, ?, 1, 1, 1, ?, ?)`,
			id, m.name, randomUIDValidity(), m.specialUse, now.Unix()); err != nil {
			return Account{}, fmt.Errorf("create default mailbox %q: %w", m.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("commit create account: %w", err)
	}
	return Account{
		ID: id, PrimaryAddress: address, DisplayName: displayName,
		PasswordHash: passwordHash, CreatedAt: now,
	}, nil
}

type defaultMailbox struct {
	name       string
	specialUse string
}

func defaultMailboxes() []defaultMailbox {
	return []defaultMailbox{
		{name: "INBOX"},
		{name: "Sent", specialUse: `\Sent`},
		{name: "Drafts", specialUse: `\Drafts`},
		{name: "Trash", specialUse: `\Trash`},
		{name: "Junk", specialUse: `\Junk`},
		{name: "Archive", specialUse: `\Archive`},
	}
}

// ListAccounts returns every account ordered by address.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT id, primary_address, display_name, password_hash, disabled, created_at
		FROM accounts ORDER BY primary_address`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var accounts []Account
	for rows.Next() {
		var a Account
		var created int64
		if err := rows.Scan(&a.ID, &a.PrimaryAddress, &a.DisplayName, &a.PasswordHash, &a.Disabled, &created); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		a.CreatedAt = time.Unix(created, 0).UTC()
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// AccountByID returns one account.
func (s *Store) AccountByID(ctx context.Context, id int64) (Account, error) {
	return s.scanAccount(s.reader.QueryRowContext(ctx, `
		SELECT id, primary_address, display_name, password_hash, disabled, created_at
		FROM accounts WHERE id = ?`, id))
}

// AccountByAddress returns the account owning address, including aliases and
// catch-all domains.
func (s *Store) AccountByAddress(ctx context.Context, address string) (Account, error) {
	a, _, err := s.ResolveRecipient(ctx, address)
	return a, err
}

// ResolveRecipient maps an envelope recipient to an account. It tries an exact
// address, then the plus-tag-stripped form, then a domain catch-all.
func (s *Store) ResolveRecipient(ctx context.Context, address string) (Account, string, error) {
	address, err := NormalizeAddress(address)
	if err != nil {
		return Account{}, "", err
	}
	a, matched, err := s.resolveExact(ctx, address)
	if err == nil {
		return a, matched, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Account{}, "", err
	}
	if base, ok := stripPlus(address); ok && base != address {
		a, matched, err = s.resolveExact(ctx, base)
		if err == nil {
			return a, matched, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Account{}, "", err
		}
	}
	if domain := domainOf(address); domain != "" {
		a, matched, err = s.resolveExact(ctx, "*@"+domain)
		if err == nil {
			return a, matched, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Account{}, "", err
		}
	}
	return Account{}, "", ErrNoRecipient
}

func (s *Store) resolveExact(ctx context.Context, address string) (Account, string, error) {
	var accountID int64
	err := s.reader.QueryRowContext(ctx, `SELECT account_id FROM addresses WHERE address = ?`, address).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, "", ErrNotFound
	}
	if err != nil {
		return Account{}, "", fmt.Errorf("lookup address: %w", err)
	}
	a, err := s.AccountByID(ctx, accountID)
	if err != nil {
		return Account{}, "", err
	}
	if a.Disabled {
		return Account{}, "", ErrAccountDisabled
	}
	return a, address, nil
}

func (s *Store) scanAccount(row *sql.Row) (Account, error) {
	var a Account
	var created int64
	err := row.Scan(&a.ID, &a.PrimaryAddress, &a.DisplayName, &a.PasswordHash, &a.Disabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("scan account: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, nil
}

// SetPasswordHash replaces an account's password hash.
func (s *Store) SetPasswordHash(ctx context.Context, accountID int64, hash string) error {
	res, err := s.writer.ExecContext(ctx, `UPDATE accounts SET password_hash = ? WHERE id = ?`, hash, accountID)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAccountDisabled enables or disables an account.
func (s *Store) SetAccountDisabled(ctx context.Context, accountID int64, disabled bool) error {
	res, err := s.writer.ExecContext(ctx, `UPDATE accounts SET disabled = ? WHERE id = ?`, disabled, accountID)
	if err != nil {
		return fmt.Errorf("set account disabled: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddAlias adds an address alias to accountID.
func (s *Store) AddAlias(ctx context.Context, accountID int64, address string) error {
	address, err := NormalizeAddress(address)
	if err != nil {
		return err
	}
	if strings.HasPrefix(address, "*@") {
		return errors.New("store: use AddCatchAll for wildcard addresses")
	}
	_, err = s.writer.ExecContext(ctx, `
		INSERT INTO addresses (address, account_id, kind, created_at) VALUES (?, ?, 'alias', ?)`,
		address, accountID, time.Now().UTC().Unix())
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: address %s", ErrDuplicate, address)
		}
		return fmt.Errorf("add alias: %w", err)
	}
	return nil
}

// AddCatchAll routes every otherwise-unmatched address at domain to accountID.
func (s *Store) AddCatchAll(ctx context.Context, accountID int64, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, "@")))
	if domain == "" || strings.ContainsAny(domain, " /\\") {
		return fmt.Errorf("store: invalid catch-all domain %q", domain)
	}
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO addresses (address, account_id, kind, created_at) VALUES (?, ?, 'catchall', ?)`,
		"*@"+domain, accountID, time.Now().UTC().Unix())
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: catch-all %s", ErrDuplicate, domain)
		}
		return fmt.Errorf("add catch-all: %w", err)
	}
	return nil
}

// RemoveAddress removes an alias or catch-all. Primary addresses cannot be
// removed.
func (s *Store) RemoveAddress(ctx context.Context, address string) error {
	address = strings.ToLower(strings.TrimSpace(address))
	res, err := s.writer.ExecContext(ctx, `DELETE FROM addresses WHERE address = ? AND kind <> 'primary'`, address)
	if err != nil {
		return fmt.Errorf("remove address: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAddresses returns all addresses for an account.
func (s *Store) ListAddresses(ctx context.Context, accountID int64) ([]string, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT address FROM addresses WHERE account_id = ? ORDER BY kind, address`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list addresses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, fmt.Errorf("scan address: %w", err)
		}
		out = append(out, address)
	}
	return out, rows.Err()
}

// NormalizeAddress lowercases and canonicalizes an RFC 5322 mailbox.
func NormalizeAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("empty address")
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil {
		return "", fmt.Errorf("parse address %q: %w", address, err)
	}
	if parsed.Address == "" || !strings.Contains(parsed.Address, "@") {
		return "", fmt.Errorf("invalid address %q", address)
	}
	return strings.ToLower(strings.TrimSpace(parsed.Address)), nil
}

func stripPlus(address string) (string, bool) {
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return address, false
	}
	local := address[:at]
	plus := strings.LastIndexByte(local, '+')
	if plus < 0 {
		return address, false
	}
	return local[:plus] + address[at:], true
}

func domainOf(address string) string {
	at := strings.LastIndexByte(address, '@')
	if at < 0 || at == len(address)-1 {
		return ""
	}
	return address[at+1:]
}

func randomUIDValidity() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint32(time.Now().Unix())
	}
	v := binary.BigEndian.Uint32(b[:])
	if v == 0 {
		v = 1
	}
	return v
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}
