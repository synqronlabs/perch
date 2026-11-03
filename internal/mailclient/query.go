package mailclient

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
)

// ParseQuery converts a compact, shell-friendly search expression into IMAP
// search criteria. Bare words are matched against the message text. Supported
// keys are from, to, cc, bcc, subject, body, text, header:Name=Value, since,
// before, on, sent-since, sent-before, sent-on, seen, unseen, answered,
// unanswered, flagged, unflagged, deleted, undeleted, draft, undraft, larger,
// smaller, uid, flag/keyword, and unflag/unkeyword.
func ParseQuery(query string) (*imap.SearchCriteria, error) {
	tokens, err := splitQuery(query)
	if err != nil {
		return nil, err
	}
	criteria := &imap.SearchCriteria{}
	for _, token := range tokens {
		switch strings.ToLower(token) {
		case "all", "*":
			continue
		case "seen":
			criteria.Flag = append(criteria.Flag, imap.FlagSeen)
			continue
		case "unseen":
			criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
			continue
		case "answered":
			criteria.Flag = append(criteria.Flag, imap.FlagAnswered)
			continue
		case "unanswered":
			criteria.NotFlag = append(criteria.NotFlag, imap.FlagAnswered)
			continue
		case "flagged":
			criteria.Flag = append(criteria.Flag, imap.FlagFlagged)
			continue
		case "unflagged":
			criteria.NotFlag = append(criteria.NotFlag, imap.FlagFlagged)
			continue
		case "deleted":
			criteria.Flag = append(criteria.Flag, imap.FlagDeleted)
			continue
		case "undeleted":
			criteria.NotFlag = append(criteria.NotFlag, imap.FlagDeleted)
			continue
		case "draft":
			criteria.Flag = append(criteria.Flag, imap.FlagDraft)
			continue
		case "undraft":
			criteria.NotFlag = append(criteria.NotFlag, imap.FlagDraft)
			continue
		}

		key, value, ok := strings.Cut(token, ":")
		if !ok {
			criteria.Text = append(criteria.Text, token)
			continue
		}
		key = strings.ToLower(key)
		switch key {
		case "from", "to", "cc", "bcc", "subject":
			if value == "" {
				return nil, fmt.Errorf("%s: search value is empty", key)
			}
			criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{
				Key:   strings.ToUpper(key),
				Value: value,
			})
		case "body":
			if value == "" {
				return nil, errors.New("body: search value is empty")
			}
			criteria.Body = append(criteria.Body, value)
		case "text":
			if value == "" {
				return nil, errors.New("text: search value is empty")
			}
			criteria.Text = append(criteria.Text, value)
		case "header":
			name, headerValue, found := strings.Cut(value, "=")
			if !found || strings.TrimSpace(name) == "" {
				return nil, errors.New("header searches use header:Name=Value")
			}
			criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{
				Key:   name,
				Value: headerValue,
			})
		case "since", "before", "on", "sent-since", "sent-before", "sent-on":
			date, err := parseSearchDate(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			setCriteriaDate(criteria, key, date)
		case "larger":
			size, err := parseSize(value)
			if err != nil {
				return nil, fmt.Errorf("larger: %w", err)
			}
			criteria.Larger = size
		case "smaller":
			size, err := parseSize(value)
			if err != nil {
				return nil, fmt.Errorf("smaller: %w", err)
			}
			criteria.Smaller = size
		case "uid":
			set, err := ParseUIDSet(value)
			if err != nil {
				return nil, fmt.Errorf("uid: %w", err)
			}
			criteria.UID = append(criteria.UID, set)
		case "flag", "keyword":
			if value == "" {
				return nil, fmt.Errorf("%s: search value is empty", key)
			}
			criteria.Flag = append(criteria.Flag, imap.Flag(value))
		case "unflag", "unkeyword":
			if value == "" {
				return nil, fmt.Errorf("%s: search value is empty", key)
			}
			criteria.NotFlag = append(criteria.NotFlag, imap.Flag(value))
		default:
			return nil, fmt.Errorf("unknown search key %q", key)
		}
	}
	return criteria, nil
}

// ParseUIDSet parses an IMAP UID set such as "1,3:5,10:*". A zero value means
// "*" in the go-imap API.
func ParseUIDSet(value string) (imap.UIDSet, error) {
	var set imap.UIDSet
	value = strings.TrimSpace(value)
	if value == "" {
		return set, errors.New("UID set is empty")
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return set, fmt.Errorf("invalid UID set %q", value)
		}
		startText, stopText, isRange := strings.Cut(part, ":")
		start, err := parseUID(startText)
		if err != nil {
			return set, fmt.Errorf("invalid UID set %q: %w", value, err)
		}
		if !isRange {
			set.AddNum(start)
			continue
		}
		stop, err := parseUID(stopText)
		if err != nil {
			return set, fmt.Errorf("invalid UID set %q: %w", value, err)
		}
		if start == 0 {
			return set, fmt.Errorf("invalid UID set %q: a range cannot start with *", value)
		}
		set.AddRange(start, stop)
	}
	return set, nil
}

func parseUID(value string) (imap.UID, error) {
	value = strings.TrimSpace(value)
	if value == "*" {
		return 0, nil
	}
	number, err := strconv.ParseUint(value, 10, 32)
	if err != nil || number == 0 {
		return 0, fmt.Errorf("invalid UID %q", value)
	}
	return imap.UID(number), nil
}

func parseSearchDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("date is empty")
	}
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q (use YYYY-MM-DD)", value)
	}
	return date, nil
}

func setCriteriaDate(criteria *imap.SearchCriteria, key string, date time.Time) {
	switch key {
	case "since":
		criteria.Since = date
	case "before":
		criteria.Before = date
	case "on":
		criteria.Since = date
		criteria.Before = date.Add(24 * time.Hour)
	case "sent-since":
		criteria.SentSince = date
	case "sent-before":
		criteria.SentBefore = date
	case "sent-on":
		criteria.SentSince = date
		criteria.SentBefore = date.Add(24 * time.Hour)
	}
}

func parseSize(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" {
		return 0, errors.New("size is empty")
	}
	multiplier := int64(1)
	switch value[len(value)-1] {
	case 'K':
		multiplier = 1 << 10
		value = value[:len(value)-1]
	case 'M':
		multiplier = 1 << 20
		value = value[:len(value)-1]
	case 'G':
		multiplier = 1 << 30
		value = value[:len(value)-1]
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("invalid size %q", value)
	}
	return size * multiplier, nil
}

func splitQuery(query string) ([]string, error) {
	var tokens []string
	var token strings.Builder
	var quote rune
	escaped := false
	inToken := false
	flush := func() {
		if inToken {
			tokens = append(tokens, token.String())
			token.Reset()
			inToken = false
		}
	}

	for _, r := range query {
		if escaped {
			token.WriteRune(r)
			escaped = false
			inToken = true
			continue
		}
		if r == '\\' {
			escaped = true
			inToken = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			inToken = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			inToken = true
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		token.WriteRune(r)
		inToken = true
	}
	if escaped {
		return nil, errors.New("query ends with an incomplete escape")
	}
	if quote != 0 {
		return nil, errors.New("query has an unterminated quote")
	}
	flush()
	return tokens, nil
}
