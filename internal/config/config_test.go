package config

import "testing"

func TestS3KeyTemplateValidation(t *testing.T) {
	base := Default()
	base.Storage.Mode = StorageS3
	base.Storage.S3.Bucket = "bucket"
	base.Storage.S3.SecretKey = "secret"
	base.Storage.S3.Prefix = "perch/"
	base.Storage.S3.KeyTemplate = "perch/{{.recipient.local}}/{{.recipient.domain}}/{{.task.id}}.eml"
	if err := base.Validate(); err != nil {
		t.Fatalf("recipient template rejected: %v", err)
	}
	base.Storage.S3.KeyTemplate = "perch/{{.mailbox.address}}/{{.task.id}}.eml"
	if err := base.Validate(); err != nil {
		t.Fatalf("legacy mailbox template rejected: %v", err)
	}
	base.Storage.S3.KeyTemplate = "perch/{{.recipient.local}}/{{.task.id}}.eml"
	if err := base.Validate(); err == nil {
		t.Fatal("template without domain was accepted")
	}
	base.Storage.S3.KeyTemplate = "other/{{.recipient.local}}/{{.recipient.domain}}/{{.task.id}}.eml"
	if err := base.Validate(); err == nil {
		t.Fatal("template outside the configured prefix was accepted")
	}
}
