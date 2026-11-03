package mailclient

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		check func(t *testing.T, criteria *imap.SearchCriteria)
	}{
		{
			name:  "empty",
			query: "",
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if len(criteria.Header) != 0 || len(criteria.Text) != 0 {
					t.Fatalf("criteria = %+v", criteria)
				}
			},
		},
		{
			name:  "headers flags and size",
			query: `from:alice subject:"Quarterly report" unseen larger:1M`,
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if len(criteria.Header) != 2 {
					t.Fatalf("headers = %+v", criteria.Header)
				}
				if criteria.Header[0].Key != "FROM" || criteria.Header[0].Value != "alice" {
					t.Fatalf("from = %+v", criteria.Header[0])
				}
				if criteria.Header[1].Key != "SUBJECT" || criteria.Header[1].Value != "Quarterly report" {
					t.Fatalf("subject = %+v", criteria.Header[1])
				}
				if len(criteria.NotFlag) != 1 || criteria.NotFlag[0] != imap.FlagSeen {
					t.Fatalf("not flags = %+v", criteria.NotFlag)
				}
				if criteria.Larger != 1<<20 {
					t.Fatalf("larger = %d", criteria.Larger)
				}
			},
		},
		{
			name:  "dates",
			query: "since:2026-01-02 before:2026-02-01",
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if got := criteria.Since.Format("2006-01-02"); got != "2026-01-02" {
					t.Fatalf("since = %s", got)
				}
				if got := criteria.Before.Format("2006-01-02"); got != "2026-02-01" {
					t.Fatalf("before = %s", got)
				}
			},
		},
		{
			name:  "on date sets a one day window",
			query: "on:2026-03-04",
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if criteria.Before.Sub(criteria.Since) != 24*time.Hour {
					t.Fatalf("window = %v", criteria.Before.Sub(criteria.Since))
				}
			},
		},
		{
			name:  "sent on date sets a one day window",
			query: "sent-on:2026-03-04",
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if criteria.SentBefore.Sub(criteria.SentSince) != 24*time.Hour {
					t.Fatalf("window = %v", criteria.SentBefore.Sub(criteria.SentSince))
				}
			},
		},
		{
			name:  "uid set",
			query: "uid:1,3:5,*",
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if len(criteria.UID) != 1 {
					t.Fatalf("uid criteria = %+v", criteria.UID)
				}
				set := criteria.UID[0]
				if !set.Contains(1) || !set.Contains(4) {
					t.Fatalf("uid set = %s", set)
				}
			},
		},
		{
			name:  "header",
			query: "header:X-Tag=value",
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if len(criteria.Header) != 1 || criteria.Header[0].Key != "X-Tag" || criteria.Header[0].Value != "value" {
					t.Fatalf("headers = %+v", criteria.Header)
				}
			},
		},
		{
			name:  "keyword",
			query: "flag:$junk unkeyword:$notjunk",
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if len(criteria.Flag) != 1 || criteria.Flag[0] != imap.Flag("$junk") {
					t.Fatalf("flags = %+v", criteria.Flag)
				}
				if len(criteria.NotFlag) != 1 || criteria.NotFlag[0] != imap.Flag("$notjunk") {
					t.Fatalf("not flags = %+v", criteria.NotFlag)
				}
			},
		},
		{
			name:  "bare terms",
			query: "hello world",
			check: func(t *testing.T, criteria *imap.SearchCriteria) {
				if len(criteria.Text) != 2 || criteria.Text[0] != "hello" || criteria.Text[1] != "world" {
					t.Fatalf("text = %+v", criteria.Text)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			criteria, err := ParseQuery(test.query)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, criteria)
		})
	}
}

func TestParseQueryErrors(t *testing.T) {
	for _, query := range []string{
		`from:"unterminated`,
		`unknown:value`,
		`larger:huge`,
		`since:not-a-date`,
		`uid:`,
	} {
		t.Run(strings.ReplaceAll(query, ":", "_"), func(t *testing.T) {
			if _, err := ParseQuery(query); err == nil {
				t.Fatalf("ParseQuery(%q) succeeded", query)
			}
		})
	}
}

func TestParseUIDSet(t *testing.T) {
	set, err := ParseUIDSet("1,3:5,10:*")
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []uint32{1, 3, 4, 5, 10, 1000} {
		if !set.Contains(imap.UID(uid)) {
			t.Fatalf("set %s does not contain %d", set, uid)
		}
	}
	if set.Contains(2) {
		t.Fatalf("set %s contains 2", set)
	}
	if _, err := ParseUIDSet("*:5"); err == nil {
		t.Fatal("range starting with * was accepted")
	}
	if _, err := ParseUIDSet("*,2"); err != nil {
		t.Fatalf("standalone * was rejected: %v", err)
	}
	if _, err := ParseUIDSet("0"); err == nil {
		t.Fatal("UID 0 was accepted")
	}
}
