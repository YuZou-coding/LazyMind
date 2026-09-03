package localworkspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"lazymind/core/common"
	"lazymind/core/common/orm"
)

func TestCurrentDirectoryIdentityChangesWhenDirectoryIsReplaced(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "workspace")
	replacement := filepath.Join(root, "replacement")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	originalIdentity, err := currentDirectoryIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, target); err != nil {
		t.Fatal(err)
	}
	replacementIdentity, err := currentDirectoryIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	if originalIdentity == replacementIdentity {
		t.Fatalf("directory replacement retained identity %q", originalIdentity)
	}
}

func TestValidateCurrentDirectoryPermanentlyMarksReplacementUnavailable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&orm.LocalWorkspace{}); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	now := time.Now().UTC()
	row := orm.LocalWorkspace{
		ID: "lws_test", CreateUserID: "user", DisplayName: "workspace",
		CanonicalPath: path, DirectoryIdentity: "fsid:invalid:0:0",
		Status: StatusActive, Version: 1, Source: "local",
		ReadPolicy: ReadPolicyAllow, WritePolicy: WritePolicyAsk,
		AuthorizedAt: now, LastUsedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	err = ValidateCurrentDirectory(t.Context(), db, row)
	appErr, ok := err.(*common.AppError)
	if !ok || appErr.Code != 2002330 {
		t.Fatalf("expected path unavailable app error, got %#v", err)
	}
	var stored orm.LocalWorkspace
	if err := db.First(&stored, "id = ?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusPathUnavailable || stored.Version != 2 {
		t.Fatalf("unexpected stored state: status=%q version=%d", stored.Status, stored.Version)
	}
}
