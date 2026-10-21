package ingest

import (
	"errors"
	"fmt"
	"strings"

	"github.com/synqronlabs/perch/internal/store"
)

// keyPart is one literal or {{.variable}} segment of an S3 object key template.
type keyPart struct {
	literal  string
	variable string
}

// mailboxAddressFromKey extracts the recipient address from an mxRaven S3 key.
// It supports the documented mailbox.* and recipient.* variable families and
// ignores other variables such as task.id and date.*.
func mailboxAddressFromKey(template, key string) (string, error) {
	parts, err := parseKeyTemplate(template)
	if err != nil {
		return "", err
	}
	values := make(map[string]string)
	remaining := key
	for i, part := range parts {
		if part.variable == "" {
			if !strings.HasPrefix(remaining, part.literal) {
				return "", fmt.Errorf("key %q does not match template %q", key, template)
			}
			remaining = remaining[len(part.literal):]
			continue
		}
		next := nextLiteral(parts, i+1)
		if next == "" {
			values[part.variable] = remaining
			remaining = ""
			continue
		}
		index := strings.Index(remaining, next)
		if index < 0 {
			return "", fmt.Errorf("key %q does not match template %q", key, template)
		}
		values[part.variable] = remaining[:index]
		remaining = remaining[index:]
	}
	if remaining != "" {
		return "", fmt.Errorf("key %q has trailing data for template %q", key, template)
	}
	var address string
	switch {
	case values["mailbox.address"] != "":
		address = values["mailbox.address"]
	case values["recipient.local"] != "" && values["recipient.domain"] != "":
		address = values["recipient.local"] + "@" + values["recipient.domain"]
	case values["mailbox.local_part"] != "" && values["mailbox.domain"] != "":
		address = values["mailbox.local_part"] + "@" + values["mailbox.domain"]
	default:
		return "", errors.New("key template does not identify a mailbox address")
	}
	return store.NormalizeAddress(address)
}

func parseKeyTemplate(template string) ([]keyPart, error) {
	var parts []keyPart
	for {
		start := strings.Index(template, "{{.")
		if start < 0 {
			if template != "" {
				parts = append(parts, keyPart{literal: template})
			}
			break
		}
		if start > 0 {
			parts = append(parts, keyPart{literal: template[:start]})
		}
		end := strings.Index(template[start:], "}}")
		if end < 0 {
			return nil, errors.New("unterminated template variable")
		}
		name := strings.TrimSpace(template[start+3 : start+end])
		if name == "" {
			return nil, errors.New("empty template variable")
		}
		parts = append(parts, keyPart{variable: name})
		template = template[start+end+2:]
	}
	if len(parts) == 0 {
		return nil, errors.New("empty key template")
	}
	return parts, nil
}

func nextLiteral(parts []keyPart, from int) string {
	for i := from; i < len(parts); i++ {
		if parts[i].literal != "" {
			return parts[i].literal
		}
	}
	return ""
}
